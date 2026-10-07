#!/usr/bin/env bash
# MISSION: 复算骨架 tag 是否真的到了每个推送目标——「我推过」不是证据，对端列出来才是。
# USAGE:   scripts/check-tag-sync.sh [起始版本号]   # 默认全量；给数字则只核 >= 该号，如 92
# GUARDRAILS: 只读（ls-remote / 本地 ref 查询），零写、零推送；目标不可达退出码 2，不冒充"通过"。
#
# 判据出处：docs/运维/构建与部署.md 换装三查的第 4 条（tag 也算产物，推过要按远端复算）。
# 动因是两次相反方向的失真：v93 推给了私有就写成"tag 已推"（公开侧其实没有），
# v88 在本地打过就当成远端有（公开侧根本没有，链呈 v87 → v89）。
#
# **按推送 URL 展开，而不是按 remote 名**：origin 配了两个 push URL（gitee 与 github），
# 而 `ls-remote <remote>` 问的是 fetch URL——只看 remote 名会漏核其中一个公开侧，
# 恰好就是这条脚本要防的那件事（首跑即被抓到，故写成这样）。
#
# 刻意**不接进 `make vet` / `make build`**：那会让每次构建发 N 次网络请求，
# 离线或远端抖动就构建假红，而假红的门禁第二次就被无视（判据页反复写过）。
# 它是"推 tag 的当下"手动跑的一条（`make tag-sync-check`），不是每颗提交都跑的闸门。
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2

# 默认只核 **v86 起**：v1~v53 从未推过任何远端（本地独有），v54~v85 只进过 gitee（github 还漏了
# v56/v74/v75/v76/v77，私有侧整段没有），三处齐全这条规则是从 v86 才开始成立的。
# 起点写死在这里并说明依据，而不是让它默认报 85 颗红——**没人会跑一条开场就全红的检查**。
# 要看历史全量：`scripts/check-tag-sync.sh --all`（它会把上面那段历史一起列出来，用于补推时核对）。
if [ "${1:-}" = "--all" ]; then
  FROM=0
else
  FROM="${1:-86}"
fi
bad=0
TARGETS=()   # 形如 label|url
for r in $(git remote); do
  n=0
  while read -u 3 -r url; do
    case "$url" in
      /*|file:*|.*) echo "跳过 $r 的本地路径目标（$url）——它不是发布目标。" ; continue ;;
    esac
    n=$((n+1))
    # 主机名要**先切第一段再剥凭据**：`git@gitee.com:syiism/x.git` 的可见标识是 gitee.com，
    # 而不是用户名 syiism——上一版两条 sed 顺序反了，两个公开目标都显示成 origin#syiism，
    # 于是"两列都齐"看起来像核过两个地方，其实读者无法分辨是哪两个（列出来却分不开 = 没列）。
    host=$(printf '%s' "$url" | sed -E -e 's#^(ssh\+git|ssh|git\+)://##' -e 's#^[^@/]+@##' -e 's#[:/].*##')
    case "$host" in
      *..*|'') host="$url" ;;   # 解析不出来就退回整条 URL，宁可难看也别错标
    esac
    multi=$(git remote get-url --push --all "$r" | wc -l)
    if [ "$multi" -gt 1 ]; then label="$r#$host"; else label="$r($host)"; fi
    TARGETS+=("$label|$url")
  done 3< <(git remote get-url --push --all "$r")
done

if [ "${#TARGETS[@]}" -eq 0 ]; then
  echo "没有任何推送目标——本检查没有对象（不是通过）。" >&2
  exit 2
fi

TAGS=()
while read -r name; do
  case "$name" in skeleton-v*) ;; *) continue ;; esac
  num="${name#skeleton-v}"; num="${num%b}"
  [ "$num" -ge "$FROM" ] 2>/dev/null || continue
  TAGS+=("$name")
done < <(git for-each-ref --format='%(refname:short)' refs/tags)

if [ "${#TAGS[@]}" -eq 0 ]; then
  echo "本地没有匹配的 skeleton tag（>= $FROM）——本检查没有对象（不是通过）。" >&2
  exit 2
fi

# 一次性拉每个目标的 tag 表；不可达在这里就定性，不留到后面静默跳过。
declare -A TTAG TST
for t in "${TARGETS[@]}"; do
  label="${t%%|*}"; url="${t#*|}"
  if out=$(git ls-remote --tags "$url" 2>/dev/null); then
    TST[$label]=ok; TTAG[$label]=$out
  else
    echo "取不到 $label 的 tag 表——网络或权限问题，**不算通过**。" >&2
    TST[$label]=down
  fi
done

printf '%-14s %-10s' 'tag' '本地'
for t in "${TARGETS[@]}"; do printf '%-26s' "${t%%|*}"; done
printf '%s\n' '判定'

for tag in "${TAGS[@]}"; do
  lobj=$(git rev-parse --short=8 "$tag")
  lpeel=$(git rev-parse --short=8 "$tag^{commit}" 2>/dev/null || echo --------)
  line=$(printf '%-14s %-10s' "$tag" "$lobj")
  verdict=ok
  for t in "${TARGETS[@]}"; do
    label="${t%%|*}"
    if [ "${TST[$label]}" != "ok" ]; then line+=$(printf '%-26s' '不可达'); verdict=unreachable; continue; fi
    obj=$(printf '%s\n' "${TTAG[$label]}" | awk -v n="refs/tags/$tag" '$2==n{print substr($1,1,8); exit}')
    peel=$(printf '%s\n' "${TTAG[$label]}" | awk -v n="refs/tags/$tag^{}" '$2==n{print substr($1,1,8); exit}')
    if [ -z "$obj" ]; then
      line+=$(printf '%-26s' '缺'); verdict=missing
    elif [ "$obj" != "$lobj" ] || { [ -n "$peel" ] && [ "$peel" != "$lpeel" ]; }; then
      # tag object 与 peel 分两列看：附注 tag 的 ls-remote 第一列是 tag 对象本身，不是 commit。
      line+=$(printf '%-26s' "异 obj=$obj peel=${peel:-无}"); verdict=diff
    else
      line+=$(printf '%-26s' "$obj/$peel")
    fi
  done
  case "$verdict" in
    ok) line+='齐' ;;
    missing) line+='**缺**'; bad=$((bad+1)) ;;
    diff) line+='**指向不同**'; bad=$((bad+1)) ;;
    unreachable) line+='远端不可达，未核'; bad=$((bad+1)) ;;
  esac
  printf '%s\n' "$line"
done

if [ $bad -gt 0 ]; then
  echo "$bad 颗 tag 没到齐、指向不一致或未核——按列补推后重跑本脚本。" >&2
  exit 1
fi
echo "全部到位：本地与每个推送目标上，skeleton tag 的 tag object 与目标 commit 都相同（共 ${#TAGS[@]} 颗、${#TARGETS[@]} 个目标）。"
