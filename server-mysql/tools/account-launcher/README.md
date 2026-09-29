# 梦幻奇遇记多账号启动器

Windows 桌面工具，用于保存多个游戏账号并分别启动、自动登录和结束游戏。自动登录需要给本地 `client-test` 安装配套补丁。

## 使用

1. 打开 `梦幻奇遇记多账号启动器.exe`。
2. 选择包含 `梦幻奇遇记.exe` 的游戏目录。
3. 添加账号并填写账号、密码。
4. 点击对应行的“启动游戏”，或勾选账号后点击“启动所选”；“启动全部”会启动列表中全部未运行账号。客户端会直接登录，并在角色模型加载完成后调用原有“开始游戏”逻辑。
5. “全选”和“清空选择”只改变批量启动范围，勾选状态会保存在配置中。批量启动会跳过已经运行的账号，单个账号失败不会中断其他账号。
6. 点击“结束游戏”只会结束该行由启动器启动并记录的 PID。
7. 点击“结束全部”会一次结束启动器当前管理的全部游戏进程；每个 PID 仍会核对创建时间和程序路径，失败的进程会保留在对应账号行并汇总提示。

## 自动组队

1. 可在账号列表中长期标记多个“队长”。每次组队时，在“入队”列勾选 2 到 5 个账号，并确保本次所选账号中恰好有一个已标记队长。
2. 点击“自动组队”。启动器先向对应服务端提交两分钟有效的组队计划，再启动尚未运行的入队账号；已经运行的账号不会重复启动。
3. 服务端逐个严格校验所选账号密码。全员进入游戏且不在战斗或私人活动场景后，服务端退出所选角色的旧队伍、按指定队长建立新队伍，并推送原版队伍快照。
4. 勾选“只看队长”可过滤大量账号；点击对应队长行的“显示”可按其游戏 PID 恢复并切换窗口。队长标记会随账号保存，适用于同时维护多支队伍。

服务端目标不显示在界面，也不写入启动器配置。所选游戏目录的名称为 `client-test`（不区分大小写）时使用本地服，其他有效游戏目录使用固定远程服。远程地址以混淆字节保存在程序中，避免被普通明文字符串扫描直接取出；但任何客户端建立连接时，目标地址仍可通过网络连接观察，这不是可作为密钥使用的秘密。

自动组队本身不修改客户端，也不需要新的客户端补丁。自动登录和自动进入仍依赖目标客户端已经安装下述原有补丁。

配置保存在 `%APPDATA%\MHQAccountLauncher\accounts.json`。密码通过当前 Windows 用户的 DPAPI 加密后再以 Base64 保存，不会以明文写入 JSON。换 Windows 用户或重装系统后，原有密码密文通常无法解密。

启动器还会为每个由它启动的游戏保存 PID 和 Windows 进程创建时间。关闭并重新打开启动器后，它会同时核对 PID、创建时间和当前所选游戏程序的完整路径；三项一致才恢复“运行中”状态并允许“结束游戏”。游戏已经退出、PID 被系统重用或程序路径不一致时会清除旧记录，因此不会误结束同 PID 的其他程序。

## 自动登录补丁

在仓库根目录执行：

```powershell
py .\server-mysql\tools\account-launcher\client-patch\install_client_patch.py --install
py .\server-mysql\tools\account-launcher\client-patch\install_auto_enter_patch.py --install
```

恢复补丁：

```powershell
py .\server-mysql\tools\account-launcher\client-patch\install_auto_enter_patch.py --restore
py .\server-mysql\tools\account-launcher\client-patch\install_client_patch.py --restore
```

启动器只把账号密码和自动进入标志写入新游戏进程的环境。登录补丁在 `FUI_LoginStartSystem.Start` 中读取并立即清除账号密码，直接设置 FairyGUI 的输入框，再调用客户端原有的登录回调。角色页补丁在 `FUI_EnterGameStartSystem` 完成角色模型加载后清除自动进入标志，并调用“开始游戏”按钮原有的 `<Start>b__0` 回调。该流程不依赖固定延迟、窗口焦点、鼠标坐标或屏幕分辨率。

安装脚本只修改 `client-test` 的当前 config bundle 和分析副本，不会修改远程客户端。两层补丁分别创建 `.pre-launcher-direct-login.bak` 和 `.pre-launcher-auto-enter.bak` 独立回滚备份。恢复时先恢复自动进入，再按需恢复直接登录。

## 构建

```powershell
cargo build --release
```

构建产物位于 `target\release\mhq-account-launcher.exe`。
