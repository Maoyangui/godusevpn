; 佛跳墙 安装包(Inno Setup 6)
;   iscc /DAppVersion=0.2.0 /DArch=amd64 /DBinDir=..\dist\amd64 godusevpn.iss
;   离线完整版:再加 /DOfflineWebView2=1,并把 MicrosoftEdgeWebView2RuntimeInstallerX64.exe 放到 deps\
; 安装:装服务(自动启动)、客户端、命令行;缺 WebView2 时装运行时;可选登录自启客户端。
; 卸载:先卸服务(会断开连接、删 TUN 网卡),删除文件与自启项;数据目录保留(可勾选删除)。

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef Arch
  #define Arch "amd64"
#endif
#ifndef BinDir
  #define BinDir "..\dist\" + Arch
#endif
#define AppName "佛跳墙"
#define AppId "godusevpn"
#define Publisher "Maoyangui"

[Setup]
AppId={{7B1E2C4A-9D3F-4E6B-8A21-GODUSEVPN001}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#Publisher}
AppPublisherURL=https://github.com/Maoyangui/godusevpn
DefaultDirName={autopf}\{#AppId}
DefaultGroupName={#AppName}
UninstallDisplayIcon={app}\godusevpn.exe
UninstallDisplayName={#AppName}
PrivilegesRequired=admin
#if Arch == "arm64"
ArchitecturesAllowed=arm64
ArchitecturesInstallIn64BitMode=arm64
OutputBaseFilename=godusevpn-{#AppVersion}-windows-arm64-setup
#else
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
#ifdef OfflineWebView2
OutputBaseFilename=godusevpn-{#AppVersion}-windows-x64-setup-offline
#else
OutputBaseFilename=godusevpn-{#AppVersion}-windows-x64-setup
#endif
#endif
OutputDir=..\dist\installer
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\cmd\godusevpn\build\windows\icon.ico
CloseApplications=yes
RestartApplications=no
MinVersion=10.0.17763

[Languages]
Name: "zh"; MessagesFile: "ChineseSimplified.isl"
Name: "en"; MessagesFile: "compiler:Default.isl"

[CustomMessages]
zh.Autostart=登录时自动启动 {#AppName}(托盘)
en.Autostart=Start {#AppName} at login (tray)
zh.Launch=安装完成后启动 {#AppName}
en.Launch=Launch {#AppName} after install
zh.InstallingWebView2=正在安装 Microsoft Edge WebView2 运行时…
en.InstallingWebView2=Installing Microsoft Edge WebView2 Runtime…
zh.InstallingService=正在注册后台服务…
en.InstallingService=Registering the background service…
zh.RemoveData=同时删除设置、订阅缓存与日志(%s)?
en.RemoveData=Also delete settings, subscription cache and logs (%s)?

[Tasks]
Name: "autostart"; Description: "{cm:Autostart}"; Flags: checkedonce

[Files]
Source: "{#BinDir}\godusevpn.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BinDir}\godusevpn-svc.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BinDir}\godusevpn-cli.exe"; DestDir: "{app}"; Flags: ignoreversion
#ifdef OfflineWebView2
Source: "deps\MicrosoftEdgeWebView2RuntimeInstallerX64.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall; Check: not WebView2Installed
#else
Source: "deps\MicrosoftEdgeWebview2Setup.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall; Check: not WebView2Installed
#endif

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\godusevpn.exe"
Name: "{group}\卸载 {#AppName}"; Filename: "{uninstallexe}"
Name: "{group}\恢复网络(解除禁直连闸)"; Filename: "{app}\godusevpn-svc.exe"; Parameters: "guard clear --popup"; Comment: "服务起不来、网络被禁直连闸拦住时用"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\godusevpn.exe"

[Registry]
; 登录自启(当前用户):带 --minimized 只到托盘
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "godusevpn"; ValueData: """{app}\godusevpn.exe"" --minimized"; Flags: uninsdeletevalue; Tasks: autostart
; 落地页一键导入的 godusevpn:// 协议。装到 HKA(管理员安装即 HKLM),客户端已在运行时由第二个实例把链接转交过去
Root: HKA; Subkey: "Software\Classes\godusevpn"; ValueType: string; ValueName: ""; ValueData: "URL:godusevpn Protocol"; Flags: uninsdeletekey
Root: HKA; Subkey: "Software\Classes\godusevpn"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""
Root: HKA; Subkey: "Software\Classes\godusevpn\DefaultIcon"; ValueType: string; ValueName: ""; ValueData: """{app}\godusevpn.exe"",0"
Root: HKA; Subkey: "Software\Classes\godusevpn\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\godusevpn.exe"" ""%1"""

[Run]
#ifdef OfflineWebView2
Filename: "{tmp}\MicrosoftEdgeWebView2RuntimeInstallerX64.exe"; Parameters: "/silent /install"; StatusMsg: "{cm:InstallingWebView2}"; Check: not WebView2Installed; Flags: waituntilterminated
#else
Filename: "{tmp}\MicrosoftEdgeWebview2Setup.exe"; Parameters: "/silent /install"; StatusMsg: "{cm:InstallingWebView2}"; Check: not WebView2Installed; Flags: waituntilterminated
#endif
; Service registration is checked in CurStepChanged; [Run] ignores exit codes.
Filename: "{app}\godusevpn.exe"; Description: "{cm:Launch}"; Flags: nowait postinstall skipifsilent runasoriginaluser
; 应用内升级(/VERYSILENT /RELAUNCH=1):装完以原始用户身份重新拉起客户端。客户端以普通身份启动安装包,由安装包自己弹 UAC,
; 这样 Inno 才有未提权的"原始用户"进程来执行这一条
Filename: "{app}\godusevpn.exe"; Flags: nowait runasoriginaluser; Check: WantRelaunch

[UninstallRun]
Filename: "taskkill.exe"; Parameters: "/F /IM godusevpn.exe"; RunOnceId: "kill-ui"; Flags: runhidden waituntilterminated

[Code]
// The service resolves the logged-on desktop/session SID itself. Inno's
// sysuserinfoname is Windows RegisteredOwner, not the original UAC user.
// A failed registration must not appear as a successful installation.
procedure CurStepChanged(CurStep: TSetupStep);
var rc: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    // 装不上服务时**不要** RaiseException:那会让 Inno 回滚、把刚装好的文件(包括「恢复网络」
    // 那个快捷方式和 godusevpn-svc.exe)一起删掉,而闸是持久的、还留在机器上 —— 用户就停在
    // "没网 + 没有任何工具能撤闸"的死局里。而且这个安装包本来就是 PrivilegesRequired=admin,
    // 提示里那句"以管理员身份重试"根本无从执行。
    // 改成:文件留下,把话说清楚,让用户还能用「恢复网络」或者手动启动服务。
    if (not Exec(ExpandConstant('{app}\godusevpn-svc.exe'), 'install', '', SW_HIDE, ewWaitUntilTerminated, rc)) or (rc <> 0) then
      MsgBox('后台服务没能注册或启动(错误码 ' + IntToStr(rc) + ')。程序文件已经装好，现有隐私保护也没有被撤销。'#13#10#13#10'如果现在上不了网，用开始菜单里的「恢复网络」把全局禁直连闸解除；之后可以再打开客户端重试。', mbError, MB_OK);
  end;
end;

// 撤闸必须成功后才能让卸载程序删除服务与工具文件。
// 不能只放在 [UninstallRun]:Inno 会忽略子进程的非零退出码,导致"闸还在但
// godusevpn-svc.exe 已被删",用户既不能恢复网络也不能重试。
//
// 注意这道门守的**只有闸**:闸还在 = 机器断网,删掉工具等于把人锁死,拦住卸载是对的。
// 网卡 IPv6 没还原不属于这一类(顶多是某几张网卡没有 v6,网照样能上),
// godusevpn-svc.exe 那边已经改成"报出来但照常卸载",不会再把卸载永久挡住。
function InitializeUninstall(): Boolean;
var rc: Integer;
begin
  Result := True;
  if FileExists(ExpandConstant('{app}\godusevpn-svc.exe')) then
  begin
    if (not Exec(ExpandConstant('{app}\godusevpn-svc.exe'), 'uninstall', '', SW_SHOWNORMAL, ewWaitUntilTerminated, rc)) or (rc <> 0) then
    begin
      MsgBox('无法解除佛跳墙的全局禁直连闸,卸载已中止 —— 现在删掉程序的话机器会一直断网且无法恢复。'#13#10#13#10'原程序与「恢复网络」工具都保留着:先用开始菜单里的「恢复网络」(右键以管理员身份运行)把闸解除,再来卸载。', mbError, MB_OK);
      Result := False;
    end;
  end;
end;

// 应用内升级:安装包带 /RELAUNCH=1,静默装完后由 [Run] 里的 runasoriginaluser 条目重新拉起客户端
function WantRelaunch: Boolean;
begin
  Result := ExpandConstant('{param:RELAUNCH|0}') = '1';
end;

// WebView2 运行时是否已装:Evergreen 在这两个键之一
function WebView2Installed: Boolean;
var v: string;
begin
  Result := RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', v) and (v <> '') and (v <> '0.0.0.0');
  if not Result then
    Result := RegQueryStringValue(HKCU, 'Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', v) and (v <> '') and (v <> '0.0.0.0');
  if not Result then
    Result := RegQueryStringValue(HKLM, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', v) and (v <> '') and (v <> '0.0.0.0');
end;

// 升级安装:先停旧服务,文件才能覆盖
function PrepareToInstall(var NeedsRestart: Boolean): String;
var rc: Integer;
begin
  Result := '';
  if FileExists(ExpandConstant('{app}\godusevpn-svc.exe')) then
  begin
    Exec('taskkill.exe', '/F /IM godusevpn.exe', '', SW_HIDE, ewWaitUntilTerminated, rc);
    // 升级只停止服务,不能走 uninstall:卸载命令按用户明确请求会撤闸并恢复网卡 IPv6,
    // 那会在替换文件期间制造直连泄漏窗口。新版本安装后由 SCM 重新启动并接管现有闸。
    //
    // 这里跑的是**旧版**的 godusevpn-svc.exe。0.6.25-m28 及更早的 Stop() 里是
    // "if _, err := s.Control(svc.Stop); err != nil { return err }",没有容忍
    // ERROR_SERVICE_NOT_ACTIVE —— 服务本来就停着(比如用户刚从托盘退出过)时它返回非零,
    // 于是 m29 新加的这道门会把从 m28 升级的路整个挡死,还给一句"无法安全停止后台服务"。
    // 停不掉就再用 sc.exe 停一次:真的没有服务在跑就照常升级,别拿它挡住用户。
    if (not Exec(ExpandConstant('{app}\godusevpn-svc.exe'), 'stop', '', SW_HIDE, ewWaitUntilTerminated, rc)) or (rc <> 0) then
    begin
      Exec(ExpandConstant('{sys}\sc.exe'), 'stop godusevpn', '', SW_HIDE, ewWaitUntilTerminated, rc);
      // sc stop 的退出码:0 = 停止请求已发出,1062 = 服务本来就没启动,1060 = 服务不存在。
      // 这三种都说明没有正在跑的服务挡着文件;别的才是真停不掉。
      if (rc <> 0) and (rc <> 1062) and (rc <> 1060) then
      begin
        Result := '无法安全停止后台服务,升级已中止以保留全局禁直连保护。';
        exit;
      end;
      Sleep(3000); // sc stop 是异步的,给它几秒真正退出,免得文件还被占着
    end;
  end;
end;

// 卸载时问一句要不要连数据一起删
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var dir: string;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    dir := ExpandConstant('{commonappdata}\godusevpn');
    if DirExists(dir) then
      if MsgBox(Format(CustomMessage('RemoveData'), [dir]), mbConfirmation, MB_YESNO) = IDYES then
        DelTree(dir, True, True, True);
  end;
end;
