#!/usr/bin/env python3
"""Apply the OmniAPI public-identifier patch to a pinned New API checkout.

Why a script instead of a hand-edited tree: this patch has to survive upstream
upgrades. Every edit declares its anchor and the number of times it must match,
so a rebase onto a newer tag fails loudly instead of silently editing the wrong
place or skipping an edit.

Target: New API v1.0.0-rc.40 (detached HEAD 0aec08f).
Run:    python3 apply_public_id.py [--source /opt/new-api-src] [--check]

What it changes:
  common/publicid.go   new file: GeneratePublicId() -> "usr_" + 26 chars
  model/user.go        PublicId column, generation on insert, self-lookup column,
                       idempotent backfill helper
  model/main.go        call the backfill from migrateDB()
  controller/user.go   expose public_id on /api/user/self
"""
import argparse
import pathlib
import sys

TOUCHED = []

FIELD_ANCHOR = (
    '\tAffCode              string                     '
    '`json:"aff_code" gorm:"type:varchar(32);column:aff_code;uniqueIndex"`\n'
)
FIELD_ADDED = FIELD_ANCHOR + (
    '\tPublicId             *string                    '
    '`json:"public_id" gorm:"type:varchar(64);column:public_id;uniqueIndex"`\n'
)

LOCK_ANCHOR = (
    '\tif err := ensureEmailAvailableWithTx(tx, user.Email, 0); err != nil {\n'
    '\t\treturn err\n'
    '\t}\n'
    '\tif user.Password == "" {\n'
)
LOCK_PATCHED = (
    '\tif err := ensureEmailAvailableWithTx(tx, user.Email, 0); err != nil {\n'
    '\t\treturn err\n'
    '\t}\n'
    '\t// The public identifier is the only account handle an API consumer sees\n'
    '\t// or passes around; generate it here because both Insert and InsertWithTx\n'
    '\t// funnel through this helper, and because the password early-return below\n'
    '\t// would otherwise skip passwordless (OAuth) registrations.\n'
    '\tif user.PublicId == nil {\n'
    '\t\tpublicId := common.GeneratePublicId()\n'
    '\t\tuser.PublicId = &publicId\n'
    '\t}\n'
    '\tif user.Password == "" {\n'
)

SELECT_ANCHOR = (
    '\t\t"group", "quota", "used_quota", "request_count", "aff_code", "aff_count",\n'
)
SELECT_PATCHED = SELECT_ANCHOR + '\t\t"public_id",\n'

MIGRATE_ANCHOR = (
    '\tif err := InitializeExternalIdentityClaims(); err != nil {\n'
    '\t\treturn err\n'
    '\t}\n'
)
MIGRATE_PATCHED = MIGRATE_ANCHOR + (
    '\tif err := backfillUserPublicIds(); err != nil {\n'
    '\t\treturn err\n'
    '\t}\n'
)

SELF_MAP_ANCHOR = '\t\t"aff_code":          user.AffCode,\n'
SELF_MAP_PATCHED = SELF_MAP_ANCHOR + '\t\t"public_id":         publicIdOf(user),\n'

SELF_HELPER = '''
// publicIdOf renders an account's public identifier. It is the only account
// identifier a client should ever receive, display or pass back.
func publicIdOf(user *model.User) string {
	if user == nil || user.PublicId == nil {
		return ""
	}
	return *user.PublicId
}
'''

BACKFILL = '''

// backfillUserPublicIds assigns a public identifier to accounts that predate the
// public_id column. It is idempotent: only rows whose public_id is NULL are
// touched, so every restart after the first one is a no-op. It runs inside the
// normal migration path so a deployment cannot forget it, and it verifies its
// own result so a partial backfill fails the boot instead of leaving accounts
// without a handle.
func backfillUserPublicIds() error {
	var ids []int
	if err := DB.Unscoped().Model(&User{}).
		Where("public_id IS NULL OR public_id = ?", "").
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		for attempt := 0; attempt < 8; attempt++ {
			publicId := common.GeneratePublicId()
			result := DB.Unscoped().Model(&User{}).
				Where("id = ? AND (public_id IS NULL OR public_id = ?)", id, "").
				Update("public_id", publicId)
			if result.Error == nil && result.RowsAffected == 1 {
				break
			}
		}
	}
	var remaining int64
	if err := DB.Unscoped().Model(&User{}).
		Where("public_id IS NULL OR public_id = ?", "").
		Count(&remaining).Error; err != nil {
		return err
	}
	if remaining != 0 {
		return fmt.Errorf("public identifier backfill left %d accounts without an id", remaining)
	}
	common.SysLog(fmt.Sprintf("public identifier backfill: assigned %d account ids", len(ids)))
	return nil
}
'''

PUBLICID_GO = '''package common

import (
	"crypto/rand"
	"strings"
)

// publicIdAlphabet deliberately omits 0/O, 1/l/I and other look-alikes: an
// operator reads these identifiers aloud or off a screenshot when reconciling a
// payment, so ambiguity costs real support time.
const publicIdAlphabet = "23456789abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ"

// PublicIdPrefix marks a string as an OmniAPI account identifier.
const PublicIdPrefix = "usr_"

// PublicIdLength is the number of random characters after the prefix.
const PublicIdLength = 26

// GeneratePublicId returns a fresh account identifier, e.g.
// usr_7k2m9q4x8n3p5rtvw2yzb6cf4h (30 characters, ~149 bits of entropy).
//
// This is an opaque handle, not a secret and not an authorisation token: it
// only has to be unguessable enough that accounts cannot be enumerated, and the
// unique index on the column is what actually guarantees correctness.
func GeneratePublicId() string {
	buf := make([]byte, PublicIdLength)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing means the platform RNG is unusable; falling back to
		// a process-unique value keeps registration working instead of silently
		// persisting an empty identifier.
		compact := strings.ReplaceAll(GetUUID(), "-", "")
		if len(compact) < PublicIdLength {
			compact = strings.Repeat(compact, 2)
		}
		return PublicIdPrefix + compact[:PublicIdLength]
	}
	out := make([]byte, PublicIdLength)
	for i, b := range buf {
		out[i] = publicIdAlphabet[int(b)%len(publicIdAlphabet)]
	}
	return PublicIdPrefix + string(out)
}
'''


def apply(source, check_only):
    root = pathlib.Path(source)
    if not (root / 'model' / 'user.go').exists():
        raise SystemExit('not a New API checkout: ' + str(root))

    edits = [
        ('model/user.go', 'public_id column', FIELD_ANCHOR, FIELD_ADDED, 1),
        ('model/user.go', 'generate on insert', LOCK_ANCHOR, LOCK_PATCHED, 1),
        ('model/user.go', 'self lookup selects public_id', SELECT_ANCHOR, SELECT_PATCHED, 1),
        ('model/main.go', 'run backfill during migrate', MIGRATE_ANCHOR, MIGRATE_PATCHED, 1),
        ('controller/user.go', 'expose public_id on /api/user/self',
         SELF_MAP_ANCHOR, SELF_MAP_PATCHED, 1),
    ]

    for rel, label, old, new, expected in edits:
        path = root / rel
        text = path.read_text(encoding='utf-8')
        if new in text:
            print('  = already applied: %s [%s]' % (rel, label))
            continue
        found = text.count(old)
        if found != expected:
            raise SystemExit(
                'ANCHOR FAIL %s [%s]: matched %d time(s), expected %d.\n'
                'The upstream source has probably changed; re-check the anchor '
                'before applying.' % (rel, label, found, expected))
        if check_only:
            print('  ? would patch: %s [%s]' % (rel, label))
            continue
        path.write_text(text.replace(old, new), encoding='utf-8')
        TOUCHED.append('%s [%s]' % (rel, label))
        print('  + patched: %s [%s]' % (rel, label))

    if check_only:
        return

    user_go = root / 'model' / 'user.go'
    text = user_go.read_text(encoding='utf-8')
    if 'func backfillUserPublicIds() error {' not in text:
        user_go.write_text(text.rstrip('\n') + '\n' + BACKFILL, encoding='utf-8')
        TOUCHED.append('model/user.go [backfill helper]')
        print('  + patched: model/user.go [backfill helper]')
    text = user_go.read_text(encoding='utf-8')
    if '\n\t"fmt"\n' not in text:
        marker = 'import (\n'
        if text.count(marker) != 1:
            raise SystemExit('cannot locate the import block in model/user.go')
        user_go.write_text(text.replace(marker, marker + '\t"fmt"\n', 1), encoding='utf-8')
        TOUCHED.append('model/user.go [fmt import]')
        print('  + patched: model/user.go [fmt import]')

    controller = root / 'controller' / 'user.go'
    text = controller.read_text(encoding='utf-8')
    if 'func publicIdOf(' not in text:
        # The helper is a top-level declaration, so it goes *before* the
        # function that uses it -- and before that function's doc comment, so
        # the comment keeps describing the right symbol.
        anchor = '// buildSelfUserData is the single safe dashboard-user DTO used by GetSelf,\n'
        if text.count(anchor) != 1:
            raise SystemExit('cannot locate the buildSelfUserData doc comment')
        controller.write_text(text.replace(anchor, SELF_HELPER.lstrip('\n') + '\n' + anchor, 1),
                              encoding='utf-8')
        TOUCHED.append('controller/user.go [publicIdOf helper]')
        print('  + patched: controller/user.go [publicIdOf helper]')

    publicid = root / 'common' / 'publicid.go'
    if not publicid.exists():
        publicid.write_text(PUBLICID_GO, encoding='utf-8')
        TOUCHED.append('common/publicid.go [new file]')
        print('  + created: common/publicid.go')

    print('\n%d change(s) applied to %s' % (len(TOUCHED), root))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', default='/opt/new-api-src')
    parser.add_argument('--check', action='store_true',
                        help='verify anchors without writing anything')
    args = parser.parse_args()
    apply(args.source, args.check)
    sys.exit(0)
