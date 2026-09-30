"""Enable registration and apply the advertised CNY 5 new-user trial credit."""
import secrets
import time
from gateway import API, ROOT, Stack, credentials, write_json

# Keep the signup gift stable and auditable instead of changing it whenever the
# live FX quote moves. CNY 6.7489 = USD 1 and 500,000 quota = USD 1, so CNY 5
# becomes 370,430 units (rounded down like the recharge ledger).
NEW_USER_TRIAL_CNY = 5
NEW_USER_TRIAL_QUOTA = 370430

def main():
    admin = API(Stack().url)
    admin.login(credentials(ROOT, 'admin'))
    for key in ('RegisterEnabled', 'PasswordRegisterEnabled'):
        admin.option(key, 'true')
    admin.option('QuotaForNewUser', str(NEW_USER_TRIAL_QUOTA))
    account = {'username': 'regcheck_' + str(int(time.time())),
               'password': secrets.token_urlsafe(12)}
    user = API(Stack().url)
    user.ok('/api/user/register', account)
    profile = user.login(account)
    try:
        assert profile['role'] == 1
        details = user.ok('/api/user/self')
        assert details['quota'] == NEW_USER_TRIAL_QUOTA
        current = admin.options()
        assert all(current[k] == 'true' for k in ('RegisterEnabled', 'PasswordRegisterEnabled'))
        evidence = {'status': 'PASS', 'registration': 'PASS', 'login': 'PASS',
                    'ordinary_user_role': 'PASS',
                    'initial_quota': NEW_USER_TRIAL_QUOTA,
                    'initial_credit_cny': NEW_USER_TRIAL_CNY,
                    'gift_fx_cny_per_usd': 6.7489}
    finally:
        user.ok('/api/user/self', method='DELETE')
    evidence['test_account_removed'] = True
    write_json(ROOT / 'docs/REGISTRATION_CHECK.json', evidence)
    print('Registration and login: PASS; ordinary role; CNY 5 trial credit; disposable account removed')

if __name__ == '__main__':
    main()
