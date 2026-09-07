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
OutputBaseFilename=godusevpn-{#AppVersion}-arm64-setup
#else
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
#ifdef OfflineWebView2
OutputBaseFilename=godusevpn-{#AppVersion}-x64-setup-offline
#else
OutputBaseFilename=godusevpn-{#AppVersion}-x64-setup
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
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\godusevpn.exe"

[Registry]
; 登录自启(当前用户):带 --minimized 只到托盘
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "godusevpn"; ValueData: """{app}\godusevpn.exe"" --minimized"; Flags: uninsdeletevalue; Tasks: autostart

[Run]
#ifdef OfflineWebView2
Filename: "{tmp}\MicrosoftEdgeWebView2RuntimeInstallerX64.exe"; Parameters: "/silent /install"; StatusMsg: "{cm:InstallingWebView2}"; Check: not WebView2Installed; Flags: waituntilterminated
#else
Filename: "{tmp}\MicrosoftEdgeWebview2Setup.exe"; Parameters: "/silent /install"; StatusMsg: "{cm:InstallingWebView2}"; Check: not WebView2Installed; Flags: waituntilterminated
#endif
Filename: "{app}\godusevpn-svc.exe"; Parameters: "install"; StatusMsg: "{cm:InstallingService}"; Flags: runhidden waituntilterminated
Filename: "{app}\godusevpn.exe"; Description: "{cm:Launch}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{app}\godusevpn-svc.exe"; Parameters: "uninstall"; RunOnceId: "svc-uninstall"; Flags: runhidden waituntilterminated
Filename: "taskkill.exe"; Parameters: "/F /IM godusevpn.exe"; RunOnceId: "kill-ui"; Flags: runhidden waituntilterminated

[Code]
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
    Exec(ExpandConstant('{app}\godusevpn-svc.exe'), 'uninstall', '', SW_HIDE, ewWaitUntilTerminated, rc);
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
