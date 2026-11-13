from pathlib import Path
import sys
def apply(root):
 p=Path(root)/'relaykit/dto/claude.go';s=p.read_text(encoding='utf8')
 before='''			case "tool_result":
				if media.Content != nil {
					b, _ := kitutil.Marshal(media.Content)
					texts = append(texts, string(b))
				}'''
 after='''			case "tool_result":
				omniClaudeToolResultMeta(media.Content, &texts, &fileMeta)'''
 if after not in s:
  assert s.count(before)==1,'Claude tool-result token metadata anchor drift'
  p.write_text(s.replace(before,after),encoding='utf8')
if __name__=='__main__':apply(sys.argv[1])
