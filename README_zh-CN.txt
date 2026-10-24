MaaEnd 失败任务导出器 v0.2.0（超时重试版；旧版已完成 Windows 11 实测）

用途
由 MaaEnd 的「自定义程序」任务启动，读取目标任务列表的逐任务状态，
将所有 failed 项保存为桌面上的 TXT，然后结束进程，让 MaaEnd 继续执行下一项。
不使用截图或 OCR。窗口可以被遮挡、最小化；无需滚动任务列表。
这是外部辅助程序，不需要修改 MaaEnd 安装文件。

权限与系统要求
1. Windows 11 x64，EXE 自带运行时，无需安装 Python、Go 或 .NET。
2. 本程序在你的电脑上运行时，以启动它的 Windows 用户身份访问文件。
   通常普通用户就能写入自己的桌面，不需要特意以管理员身份运行。
   实际是否允许取决于你的桌面目录权限与安全软件，必须实机检查。
3. 使用 Windows SHGetKnownFolderPath 获取实际桌面路径，支持桌面迁移和
   OneDrive 桌面，不把 C:\Users\用户名\Desktop 写死。
4. 仅向 127.0.0.1 上指定端口发送 GET 请求，不读取进程内存，不修改 MaaEnd 配置。
5. 需要 MXU 的 Web 服务提供 /api/config、/api/maa/state；任务状态必须包含
   instances -> 列表ID -> task_run_state -> statuses。
   /api/interface 用于把内部任务名称翻译为中文；此接口不可用时仍保留内部任务名。
6. 开发环境是 Linux，已完成逻辑与模拟接口测试、Windows x64 交叉编译。
   使用者已在 Windows 11、MaaEnd v2.31.0 环境中确认：程序能通过
   「自定义程序」任务正常启动、保存失败任务 TXT，并继续执行后续任务。
   其他 MaaEnd / MXU 版本尚未逐一验证，接口变化可能影响兼容性。

第一次检查（建议先完成这一小步）
1. 解压整个 ZIP 到固定位置，例如 D:\MaaEndFailureReporter。
   这里仅是示例目录，必须根据实际解压位置配置程序路径。
   压缩包内可能还有一层 maaend-failure-reporter 子文件夹，
   EXE 和 Check.cmd 位于该文件夹中。
2. 启动 MaaEnd，查看设置中的「Web 服务」，确保启用；默认端口是 12701。
   若更改服务设置，重启 MaaEnd。仅本机使用，无需开启「允许局域网访问」。
3. 最好先让目标列表运行过至少一项任务，再双击 Check.cmd。
4. 桌面应生成 MaaEnd_检查_时间戳_随机编号.txt。
   检查文件列出各任务列表名称、ID、状态格式是否兼容。
   若报告称该列表没有后端运行实例，请先执行一次该列表再检查。
5. 若桌面生成 MaaEnd_导出异常_*.txt，先按其中提示处理。
   无法写入桌面时会尝试保存到：
   %LOCALAPPDATA%\MaaEndFailureReporter
   Check.cmd 控制台会显示实际保存路径。所有位置都不可写时无法保存报告。
6. 自定义了端口时，可在命令提示符执行：
   MaaEndFailureReporter.exe --check --port 你的端口

接入 MaaEnd（以「日常」列表为例）
1. 把「自定义程序」放到所有待检查任务之后、后续操作之前。
   例如：日常游戏任务 -> 本导出器 -> 结束游戏/其他自定义程序。
2. 程序路径：点击右侧文件选择按钮，选择实际解压位置中的
   MaaEndFailureReporter.exe，建议直接选取文件，避免手动填写错误路径。
   不要填 Check.cmd；它仅用于手动检查，会暂停等待按键。
   程序路径必须指向真实存在的 EXE，示例路径需要自行替换。
3. 附加参数：
   --instance "日常"
   如果实际标签名称不同，用实际列表名称替换「日常」。
   多个标签同名时，使用检查报告中的列表 ID，例如：--instance "abc1234"
   自定义 Web 服务端口时额外添加：--port 你的端口
4. 等待退出：开启。必须等待保存完成后再继续。
5. 已运行时跳过：关闭。每次执行都应生成一份新的报告。
6. 通过 cmd 启动：关闭。直接启动此 EXE 即可。
7. 每个需要导出的列表分别添加一个自定义程序并指定各自名称/ID。
   明确指定列表可避免多标签并行或切换标签时统计错误。

输出规则
- 文件名：MaaEnd_失败任务_YYYYMMDD_HHMMSS_随机编号.txt。
- UTF-8 带 BOM，Windows 记事本可直接阅读中文；每次新建，不覆盖之前的报告。
- 失败任务按列表顺序输出，包含名称、列表位置、任务 ID。
- 同名任务保留为不同记录；红条对应 failed，勾选框是否勾选不是判断依据。
- 不把 pending、running、succeeded 或没有登记的任务当作失败。
- 统计调用时该列表当前登记的状态，不包含尚未执行的后续任务，也不汇总历史运行。
  如需检查完整的一轮，把导出任务放在这一轮待检查任务的最后。
- 无失败时明确写出“当前状态中未发现失败任务”。没有运行状态时会明确说明，
  不会声称全部成功。
- 接口、版本、列表选择等错误会生成“导出异常”报告，不伪造空清单。
- 默认异常也退出 0，让自动化流程继续；不弹出等待用户关闭的窗口。
  如需给外部调度器返回失败码，可使用 --strict；此时检测/写入错误返回 1。
  参数本身无效返回 2。是否停止后续任务由调用方决定。
- Check.cmd 是手动检查入口，带 pause；自动化一定直接调用 EXE，不调用 Check.cmd。

可选参数
--instance "列表名或ID"   指定列表；省略时仅在唯一运行中列表或单列表配置下自动选择。
--port 12701              指定本机服务端口。
--check                   输出接口、列表与写入检查报告。
--output-dir "D:/Reports" 改用已存在的指定目录。
--strict                  导出或目标目录写入失败时返回非零码。
--help                    显示参数说明。

故障定位
如果 MaaEnd 显示「自定义程序任务失败」且没有生成报告：
1. 首先检查程序路径，使用文件选择按钮重新选择实际存在的 EXE。
2. 可在命令提示符中运行该 EXE，并传入 --instance 参数进行检查。
   如果提示「系统找不到指定的路径」，说明路径有误，程序尚未启动。
3. Check.cmd 能运行，仅表示它所在目录中的 EXE 可以手动启动，
   不代表 MaaEnd 中填写的程序路径也正确。

如果失败清单与实际红条不一致，请提供：
1. MaaEnd 设置中的版本信息（包括 MXU 版本）；
2. 本程序生成的检查/导出异常报告；
3. 对应列表的任务红条截图。
不要发送整个 /api/config 原始响应，它可能包含与你的任务无关的敏感配置。

实现与验证
源代码在 src/ 中，使用 Go 标准库。
已通过五组自动测试，涵盖同名任务、列表选择、未知/缺失状态、中文输出、
只读接口调用及报告不覆盖；另完成 CLI 模拟测试，验证正常导出、异常报告、
默认退出 0、strict 退出 1 与检查入口。
Windows EXE 已编译并确认 PE32+ x86-64 格式。
使用者已在 Windows 11、MaaEnd v2.31.0 环境中完成实机验证，
确认失败任务导出及保存后继续执行的流程符合预期。
此结果仅覆盖上述实测环境，不代表所有 MaaEnd / MXU 版本均已验证。

重新编译（本地已安装 Go 时）
cd src
go test ./...
go build -buildvcs=false -trimpath -ldflags="-s -w" -o ../MaaEndFailureReporter.exe .

依据（读取于 2026-10-05，MXU main 分支，具体安装版本需实测）
https://github.com/MistEO/MXU/blob/main/src/components/TaskItem.tsx
https://github.com/MistEO/MXU/blob/main/src-tauri/src/web_server.rs
https://github.com/MistEO/MXU/blob/main/src-tauri/src/commands/types.rs
https://github.com/MistEO/MXU/blob/main/src-tauri/src/commands/system.rs
https://learn.microsoft.com/en-us/windows/win32/shell/known-folders


v0.2.0 更新说明
单次请求超时 10 秒，每个接口最多尝试 3 次，间隔 1 秒。
仅对网络故障、超时和 HTTP 408/429/500/502/503/504 重试。
整体接口读取期限为 100 秒；正常情况下立即完成。
报告附程序版本、每次请求耗时和结果，便于排查偶发故障。
原有程序路径、--instance 参数及等待退出设置不变。
本版已完成模拟接口测试和 Windows x64 交叉编译，尚待 Win11 实机验证。
旧版实测结果不代表新版已经实机验证；持续阻塞和 API 变化仍可能失败。
替换程序前，请先等待正在运行的导出器退出。
