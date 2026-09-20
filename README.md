# lazychezmoi

以 lazygit 的操作節奏管理 chezmoi：看狀態、選檔案、編輯、預覽、套用，再回到原本的位置。

支援 macOS、Linux 與原生 Windows／PowerShell。這是本機工作台；使用已安裝的 **chezmoi** 作為操作引擎，Git commit 交給 **lazygit**。Fleet 整合與個人 dotfiles 工具移植不包含在第一版。

## 建置與啟動

需要 Go **1.25+**、Git 和 chezmoi。精細腳本紀錄重設目前驗證於 chezmoi **2.69.4**；其他版本仍可使用一般原生操作，但不開放未驗證的 state 修改。lazygit 僅在使用該 action 時需要。Windows shell reload 建議 PowerShell **7.4+**。

```sh
go build -o build/ .
./build/lazychezmoi

# 安裝目前 checkout；請將 go env GOBIN／GOPATH 對應的 bin 加入 PATH
go install .
lazychezmoi
```

Windows 使用 `build/lazychezmoi.exe`。目前沒有宣告已發布的版本或套件管理器安裝渠道。

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
| `↑↓`、`jk`、`gg` / `G` | 選取、移到開頭／結尾 |
| Tab / Shift+Tab、`h` / `l` | 切換清單與預覽焦點 |
| `/` | 搜尋；Enter 接受搜尋，Esc 清除 |
| Space、`c` | 多選檔案、只看變動 |
| `e` | 編輯來源；返回後刷新 Diff |
| `a` | 套用選取檔案，排除 scripts；Scripts 頁則執行選定腳本 |
| `A` | 檢視範圍後完整 apply，包含 scripts |
| `v`、`[` / `]` | Source / Current / Rendered / Diff 預覽 |
| `f` | 背景 fetch，更新遠端資訊 |
| `u` | `chezmoi update --init`：拉取、處理新問題、套用 |
| `L` | 在目前 working tree 開 lazygit |
| `r` | 重新讀取本機狀態 |
| `:` / `?` | 搜尋 action menu／說明 |
| `q` | 退出；正常退出不 reload shell |

啟動先呈現畫面，背景 fetch 一次，不自動 pull 或 apply。Git 的 ahead／behind 與檔案部署差異是不同資訊。沒有 tracking upstream、認證不可用或讀取失敗會明確顯示；`Fetch interactively` action 可交還終端處理認證。以 `--auto-fetch=false` 關閉啟動 fetch。

一般檔案提供 `Absorb local edits (re-add)`，讓直接修改的 live config 回到來源。模板、`modify_`、`create_` 及 encrypted 不提供這個一般檔案 action；它們保留原生語義。`create_` 是 seed：既存的目標不會被普通 apply 覆蓋。

`.toml.tmpl` 的 Source 使用 TOML 加模板標記上色；`modify_` 的來源若為腳本，依 shebang／腳本類型上色，Rendered 依目標檔名上色。預覽不重新格式化檔案。原生 render／diff 可能執行模板函式或 hooks；渲染內容不持久快取。

Maintenance 提供 native init、重新詢問既有參數（`init --prompt`）、編輯 chezmoi config／config template、用 editor 開 source tree、externals 更新、完整 context 與操作結果。Editor 沿用 chezmoi 的設定及 `VISUAL`／`EDITOR` 選擇。

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
```

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

[tools]
chezmoi = "chezmoi"
git = "git"
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
```

chezmoi 整合測試使用獨立 source、config、destination、cache 和 state，不套用到使用者家目錄。未安裝 chezmoi 的環境會略過相應測試；CI 安裝固定的 2.69.4 來執行。Shell tests 使用可取得的 Bash、Zsh、Fish 與 PowerShell，驗證 caller scope、參數、失敗及 reload。

CI 包含 macOS／Linux／Windows 的 vet、race tests、build 和真實 PTY／ConPTY editor→apply 驗收。Windows 的 Python harness 需要 `pywinpty`。本機跨編譯只能證明編譯成功，不能代替原生 Windows 終端驗收。

程式分成 `internal/chezmoi`（共享操作）、`internal/tui`（模型／action registry／預覽）、`internal/cli`（命令介面）、`internal/config` 與 `internal/shell`。介面建立不執行 I/O；非同步結果依 request generation 接受，寫入結束後重新取得狀態。
