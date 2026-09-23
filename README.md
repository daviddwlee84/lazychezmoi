# lazychezmoi

以 lazygit 的操作節奏管理 chezmoi：看狀態、選檔案、編輯、預覽、套用，再回到原本的位置。

支援 macOS、Linux 與原生 Windows／PowerShell。這是本機工作台；使用已安裝的 **chezmoi** 作為操作引擎，Git commit 交給 **lazygit**。Fleet 整合與個人 dotfiles 工具移植不包含在第一版。

## Windows installation and upgrades

```powershell
scoop bucket add daviddwlee84 https://github.com/daviddwlee84/scoop-bucket
scoop install daviddwlee84/lazychezmoi
lazychezmoi upgrade --check --json
lazychezmoi upgrade --yes
```

Windows v0.2.0+ releases include amd64/arm64 ZIPs and PowerShell completion.
Scoop owns the installed executable. Upgrade verifies its receipt, current
junction, product identity and manager, then starts a private helper outside the
package and exits so Scoop can replace the executable. Interactive use opens a
progress window. A `handed-off` result confirms acceptance; only the later
`updated` or `up-to-date` result confirms successful completion.

For automation, add `--json` and run the returned `status_command` to poll the
private helper. Do not poll the installed executable during the update, because
Scoop refuses to update a running package. Once finished,
`lazychezmoi upgrade --status <operation-id> --json` reads the saved result.
If the launching host retains process lifetime control, keep that terminal open
until the final result. Interrupted, canceled, blocked and failed operations
retain their status and log paths; none claims successful rollback or falls
back to another installer. Close other instances before retrying.

`--check` is read-only and does not refresh buckets or promise a remote latest
version. Successful completion reports the version actually installed. Manually
extracted Windows ZIPs need manual replacement while closed; package ownership and Windows process guards cannot be overridden. Installing the CLI does not
configure its backends, services or credentials.

## 建置與啟動

需要 Go **1.25+**、Git 和 chezmoi。精細腳本紀錄重設目前驗證於 chezmoi **2.69.4**；其他版本仍可使用一般原生操作，但不開放未驗證的 state 修改。lazygit 僅在使用該 action 時需要。Windows shell reload 建議 PowerShell **7.4+**。

```sh
go build -o build/ .
./build/lazychezmoi

# 安裝目前 checkout；請將 go env GOBIN／GOPATH 對應的 bin 加入 PATH
go install .
lazychezmoi
```

Windows 使用 `build/lazychezmoi.exe`。GitHub tagged releases 提供 macOS／Linux amd64／arm64 的 tar.gz 與 Windows amd64／arm64 的 ZIP，附 `checksums.txt` 與 Bash／Zsh／PowerShell completions。發行與驗證方式見 [RELEASING.md](RELEASING.md)。

Homebrew 安裝可用 `lazychezmoi upgrade`：先確認目前執行檔所屬的 formula，顯示並確認後委派給 `brew upgrade`。`lazychezmoi upgrade --check --json` 只檢查安裝來源與預定指令；非互動或 JSON 模式需要 `--yes` 才會更新。其他安裝來源會提供對應指引，不會直接覆寫執行檔。這個命令不需要 chezmoi、Git 或有效設定，也不會更新 dotfiles；既有的 `lazychezmoi update` 仍是 chezmoi 的拉取／套用操作。

預設採用 chezmoi 的 source／destination，**不會因目前工作目錄而換成另一份 checkout**。操作其他來源時明確指定：

```sh
lazychezmoi --source /path/to/dotfiles
lazychezmoi --source /path/to/source --destination /path/to/destination \
  --chezmoi-config /path/to/chezmoi.toml
```

`--config` 是 lazychezmoi 的偏好設定；`--chezmoi-config` 才是 chezmoi 設定。`--working-tree`、`--chezmoi-cache`、`--persistent-state` 也可明確指定。Active context 選單能查看完整解析路徑。

## 日常操作

| 鍵 | 行為 |
| --- | --- |
| `1` / `2` / `3` | Files / Scripts / Maintenance |
| `4`、`s` | Search 頁面／輸入內容查詢 |
| `↑↓`、`jk`、`gg` / `G` | 選取、移到開頭／結尾 |
| Tab / Shift+Tab、`h` / `l` | 切換清單與預覽焦點 |
| `/` | 搜尋；Enter 接受搜尋，Esc 清除 |
| Space、`c` | 多選檔案、只看變動 |
| `e` | 編輯來源；返回後刷新 Diff |
| `a` | 套用選取檔案，排除 scripts；Scripts 頁則執行選定腳本 |
| `A` | 檢視範圍後完整 apply，包含 scripts |
| `v`、`[` / `]` | Source / Current / Rendered / Diff 預覽 |
| `H`、`n` / `N` | 區塊模式／下一個或上一個 hunk |
| `<` / `>` | 確認後 Source → Current／Current → Source 區塊複寫 |
| `U`、`z` | Undo 最近一次區塊複寫／放大預覽 |
| `m` | 切換滑鼠捕捉，關閉後可用終端原生選字 |
| `f` | 背景 fetch，更新遠端資訊 |
| `u` | `chezmoi update --init`：拉取、處理新問題、套用 |
| `L` | 在目前 working tree 開 lazygit |
| `r` | 重新讀取本機狀態 |
| `:` / `?` | 搜尋 action menu／說明 |
| `q` | 退出；正常退出不 reload shell |

啟動先列出受管檔案並顯示 Source 預覽，完整 status 在背景自動補齊；期間可先選檔、搜尋及編輯。`?` 表示差異尚未確認，首次 status 成功前不開放「只看變動」。status 失敗仍保留可用的檔案清單，按 `r` 可重試。差異更新會保留目前選取及預覽位置。

第一次檔案清單回應後，背景 fetch 一次，不自動 pull 或 apply。Git 的 ahead／behind 與檔案部署差異是不同資訊。沒有 tracking upstream、認證不可用或讀取失敗會明確顯示；`Fetch interactively` action 可交還終端處理認證。以 `--auto-fetch=false` 關閉啟動 fetch。

受管路徑沿用原生 chezmoi 的 ignore／source 語義，Files、Scripts 與內容搜尋共用本次 session 的 metadata。按 `r`、編輯返回或其他操作完成時會重新讀取；外部新增或移除檔案後也可按 `r` 更新清單。檔案內容、render 結果及完整 status 不會寫入磁碟快取。

一般檔案提供 `Absorb local edits (re-add)`，讓直接修改的 live config 回到來源。模板、`modify_`、`create_` 及 encrypted 不提供這個一般檔案 action；它們保留原生語義。`create_` 是 seed：既存的目標不會被普通 apply 覆蓋。

`.toml.tmpl` 的 Source 使用 TOML 加模板標記上色；`modify_` 的來源若為腳本，依 shebang／腳本類型上色，Rendered 依目標檔名上色。預覽不重新格式化檔案。原生 render／diff 可能執行模板函式或 hooks；渲染內容不持久快取。

Maintenance 提供 native init、重新詢問既有參數（`init --prompt`）、編輯 chezmoi config／config template、用 editor 開 source tree、externals 更新、完整 context 與操作結果。Editor 沿用 chezmoi 的設定及 `VISUAL`／`EDITOR` 選擇。

### Delta 與雙向區塊複寫

Diff 預設自動使用 PATH 中的 `delta`。缺少、逾時或執行失敗會回退內建 unified diff，保留內容並顯示目前 renderer。預覽內寬至少 100 欄時可並排；較窄時降為 unified，`z` 可放大預覽。`NO_COLOR`／`--color=never` 使用內建無色顯示。可用 `--diff-renderer=builtin` 固定舊模式。

一般部署差異清楚標示 **Before: Current → After: Rendered**。普通檔案的區塊模式則比較 **Current ↔ Source**。按 `H` 後用 `n/N` 或 Prev／Next 按鈕選區塊；`<` 把 Source 的區塊寫到 Current，`>` 把 Current 的區塊寫回 Source。確認畫面會顯示方向、接收檔案及確切區塊。

區塊複寫只處理內容，不自動 apply、不執行 apply scripts、不更新 chezmoi state；部分 live 修改可能因此顯示為 drift，之後仍可使用原生 apply 完成部署。正常 native discovery／diff 仍保留 chezmoi 的 hooks 語義。為避免原生 re-add 在 `exact_` 目錄連帶更動 siblings，該情況會停用 re-add；符合條件的區塊複寫仍只改選定檔案。

複寫限既存、彼此不同、未加密、非模板的普通 UTF-8 檔案。create／modify、symlink／hardlink、scripts、externals、二進位、新增／刪除檔案保留原生操作。每檔至多 2 MiB；超出差異計算上限時仍可查看 native diff。保留原始 LF／CRLF、BOM、最後換行與接收檔案權限。snapshot 過期或無法保留 metadata 時拒絕覆寫。

`U` 只 Undo 本次 session 最近一次區塊複寫，且檔案必須仍與寫入後相同；不覆蓋外部的新修改。Undo 的內容保留在記憶體。替換使用同目錄 transaction scratch；Windows 遇到無法完整恢復的替換錯誤時，會保留 recovery 檔案並回報路徑，不刪除最後可恢復的內容。

Delta 只負責顯示，複寫位置取自原始 snapshot，與顏色、wrap、視窗大小無關。lazychezmoi 控制 Delta 的 pager、width 與版面，不讀取 Git 中的 Delta 設定；可在自己的 `[diff]` 設定配色及 syntax theme。

### 內容搜尋與滑鼠

`/` 仍是目前 Files／Scripts 的檔名 filter。`s` 開啟獨立的 live grep：輸入停頓約 200ms 後搜尋，Enter 接受查詢並移到結果。使用 `rg`，預設 literal keyword＋smart case，也能透過 Regex 按鈕或 action 切換正則表达式。

- **Source** 搜尋來源 repo，包含 `.chezmoitemplates` 等隱藏 helper，尊重 ignore 規則並排除 `.git`／`.specstory`。
- **Current** 只搜尋受管且現存的普通檔案，不掃描整個 HOME；二進位、symlink 與不可讀取的項目會略過。
- 結果顯示檔案、行號與命中內容；預覽定位並標示命中。`e` 編輯來源檔，helper 也能直接開啟；Current 行號不會誤套到模板原始碼。
- 缺少 rg 會提示設定 `tools.rg`／安裝；結果上限 1,000 筆，截斷、部分失敗及舊查詢結果均有標示。Rendered 不做批次搜尋，搜尋內容不持久快取。

滑鼠預設開啟：點列選取、checkbox 多選、點頁籤／預覽標籤切換、右鍵開情境選單，滾輪作用於游標所在 pane。區塊、Review／Confirm／Cancel 都有可點按鈕；modal 不會讓點擊穿透。`m` 或 `--mouse=false` 關閉捕捉。

## CLI

```sh
lazychezmoi status --json
lazychezmoi files --json
lazychezmoi preview ~/.config/foo/config.toml --view rendered
lazychezmoi preview ~/.config/foo/config.toml --view diff
lazychezmoi edit ~/.config/foo/config.toml
lazychezmoi edit ~/.config/foo/config.toml --apply
lazychezmoi apply ~/.config/foo/config.toml
lazychezmoi apply                       # 全部套用，包含 scripts
lazychezmoi fetch
lazychezmoi update                      # 預設 --init=true，拉取後套用
lazychezmoi update --init=false
lazychezmoi init --prompt               # 重新詢問舊答案，不自動 apply
lazychezmoi init --prompt --promptBool 'Exact prompt text=false'
lazychezmoi re-add ~/.config/plain-file
lazychezmoi search 'editor' --scope source --json
lazychezmoi search 'theme.*dark' --scope current --regex
lazychezmoi hunks ~/.config/plain-file --json
lazychezmoi copy-hunk ~/.config/plain-file --id REVIEWED_HUNK_ID \
  --direction source-to-current --yes
lazychezmoi preview ~/.config/foo/config.toml --view diff \
  --renderer delta --layout side-by-side --width 120 --color always
```

`preview --view diff` 預設保持原始 patch 輸出；只有明確指定 `--renderer` 才格式化。`copy-hunk` 的 ID 綁定兩端 snapshot，任何相關變更都需要重新取得 ID；CLI 不保存 Undo 紀錄。

bare `lazychezmoi` 在非 TTY 顯示 help；明確 `tui`、editor 或 shell reload 必須有互動終端。非互動原生操作使用 `--no-tty`；需要回答 init 問題時使用明確的 `--promptBool`／`--promptString`／`--promptInt`／`--promptChoice`／`--promptMultichoice`，或明確選用 `--promptDefaults`。

JSON 不混入色碼或進度。退出碼：成功 0、usage／設定錯誤 2、取消 130；原生 command 失敗盡量保留子程序退出碼。

### 腳本重跑與 externals

```sh
lazychezmoi scripts list --json
lazychezmoi scripts records /absolute/source/.chezmoiscripts/run_once_after_example.sh
lazychezmoi scripts reset /absolute/source/.chezmoiscripts/run_once_after_example.sh --yes
lazychezmoi scripts apply /absolute/source/.chezmoiscripts/run_once_after_example.sh
lazychezmoi scripts apply /absolute/source/.chezmoiscripts/run_once_after_example.sh --reset --yes
lazychezmoi externals refresh
```

Scripts 頁面以 `x` 檢視／選擇要重設的紀錄，`X` 則在重設後執行。`run_once` 用內容雜湊記錄，相同內容可能共用紀錄；`run_onchange` 重設該 target 的 entry state。只刪除經檢視與重新驗證的 keys，不清空其他檔案的狀態。

重設與 apply 不是原子交易。若重設成功而執行失敗，會呈現部分結果；成功退出也不保證腳本必定執行，工具會再檢查紀錄並回報 ran／skipped／unverifiable。

`externals refresh` 是更新並套用外部來源，排除 scripts，不是清除執行紀錄或升級所有套件。externals 沒有普通可編輯來源檔案，因此不混入 Files 清單。選定腳本執行仍會保留原生 hooks；`--refresh-externals=never` 也可能在下載快取不存在時連網。

## Shell reload

Go 子程序不能替換呼叫它的 shell。先將對應整合加入你管理的 shell 設定：

```sh
# Bash
eval "$(lazychezmoi shell-init bash)"

# Zsh
eval "$(lazychezmoi shell-init zsh)"

# Fish
lazychezmoi shell-init fish | source
```

```powershell
# PowerShell
Invoke-Expression (& lazychezmoi shell-init powershell | Out-String)
```

之後 Unix 可執行：

```sh
lazychezmoi update --reload
lazychezmoi apply --reload

# 可用來替換原本的 cau / cas；工具不會自行修改你的 dotfiles
cau() { lazychezmoi update --reload "$@"; }
cas() { lazychezmoi apply --reload "$@"; }
```

Windows 需要 caller-scope dot-source，讓任意 profile 定義留在目前 session：

```powershell
. lazychezmoi update --reload
. lazychezmoi                         # TUI 也能選 Reload & Exit
```

普通 PowerShell 呼叫仍可正常使用所有其他功能；選用 reload 時會提示正確入口。PowerShell reload 依順序讀取存在的四種 profiles，並不清除先前 session 定義；Unix 使用目前 shell 的 login restart。

只有操作成功且收到精確 reload 訊號才會 reload；失敗、取消、一般退出都不會。訊號走專用通道，stdout 不會被解讀成 shell commands。更新成功但 profile 載入失敗會另行回報。

## 設定與補全

```sh
lazychezmoi config path
lazychezmoi config show
lazychezmoi config init                # 不覆蓋已有檔案
```

Unix 預設 `~/.config/lazychezmoi/config.toml`，Windows 預設 roaming AppData 下的 `lazychezmoi/config.toml`；絕對路徑的 `XDG_CONFIG_HOME` 可覆蓋。顯式 flags 優先於設定檔。

```toml
auto_fetch = true
color = "auto"                        # auto / always / never；auto 尊重 NO_COLOR
mouse = true

[diff]
renderer = "auto"                     # auto / builtin / delta
layout = "auto"                       # auto / unified / side-by-side
theme = "auto"                        # auto / dark / light
# syntax_theme = "GitHub"             # optional Delta syntax theme

[tools]
chezmoi = "chezmoi"
git = "git"
delta = "delta"                       # optional; missing tool falls back
rg = "rg"                             # required only for content search
```

用 `lazychezmoi completion bash|zsh|fish|powershell` 產生原生補全。補全與 help 不會讀取 chezmoi、渲染模板或連網。Zsh 範例（目錄須在 shell 的 `fpath` 並在 `compinit` 前設定）：

```sh
mkdir -p ~/.zfunc
lazychezmoi completion zsh > ~/.zfunc/_lazychezmoi
# 在 .zshrc 的既有 compinit／framework initialization 前加入：
fpath=(~/.zfunc $fpath)
```

## 開發與驗證

```sh
go vet ./...
go test -race ./...
go build -o build/ .
python3 scripts/pty_smoke.py build/lazychezmoi
uv run --no-project --with pyte==0.8.2 python scripts/pty_features.py build/lazychezmoi
uv run --no-project --with pyte==0.8.2 python scripts/pty_startup.py build/lazychezmoi
```

chezmoi 整合測試使用獨立 source、config、destination、cache 和 state，不套用到使用者家目錄。未安裝 chezmoi 的環境會略過相應測試；CI 安裝固定的 2.69.4 來執行。Shell tests 使用可取得的 Bash、Zsh、Fish 與 PowerShell，驗證 caller scope、參數、失敗及 reload。

CI 包含 macOS／Linux／Windows 的 vet、race tests、build 和真實 PTY／ConPTY editor→apply、mouse、hunk→Undo、live grep 驗收。Windows 的 Python harness 需要 `pywinpty`；feature harness 使用 `pyte` 檢查 ASCII 介面控制，Unicode 寬度另有 Go 測試。CI 準備 rg／delta，使相關測試實際執行。本機跨編譯只能證明編譯成功，不能代替原生 Windows 終端驗收。

Startup harness 使用一次性 fixture，刻意讓 status 延遲五秒，驗證清單、Source 預覽與操作先就緒，並分別回報畫面、可用清單、預覽及完整 status 的時間與 native command 次數。這些時間取決於機器與資料量；不對真實 dotfiles 執行 apply。

程式分成 `internal/chezmoi`（共享操作／hunk snapshots）、`internal/diffview`（delta／內建 renderer）、`internal/search`（rg／結果預覽）、`internal/tui`（模型／action registry／共用 layout）、`internal/cli`（命令介面）、`internal/config` 與 `internal/shell`。介面建立不執行 I/O；非同步結果依 request generation 接受，寫入結束後重新取得狀態。

### Source distribution size

Release assets include a rootless source archive (`lazychezmoi_<version>_source.tar.gz`)
and its checksum. Source archives omit SpecStory history and agent plan folders
using `.gitattributes`; Go module downloads omit the same evidence through nested
`go.mod` boundary markers. Build inputs, embedded resources, tests, licenses, and
skills remain available. Full Git clones retain development history.

CI builds and exercises both a real Git archive and an independently generated Go
module ZIP using the official `golang.org/x/mod` implementation. To run the check
from a committed revision:

```sh
python3 scripts/check-distribution.py
```
