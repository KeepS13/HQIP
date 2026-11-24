; First-install delivery only. In-app updates retain their signed version ZIP.
; Keep this AppId stable so repairs/upgrades share one uninstall entry.
#ifndef AppVersion
  #error AppVersion is required
#endif
#ifndef SourceRoot
  #error SourceRoot is required
#endif
#ifndef InstallerAppId
  #define InstallerAppId "HQIP.Godot.Client"
#endif

[Setup]
AppId={#InstallerAppId}
AppName=HQIP
AppVersion={#AppVersion}
AppPublisher=HQIP
DefaultDirName={localappdata}\Programs\HQIP
DefaultGroupName=HQIP
DisableProgramGroupPage=yes
DisableDirPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputBaseFilename=HQIP-Windows-x64-Setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\..\desktop\assets\hqip.ico
UninstallDisplayIcon={app}\hqip.ico
AppMutex=Local\HQIP-Godot
SetupMutex=HQIP.Godot.Setup
; Closing the UI only hides it to tray. Require explicit normal exit so the
; native companion restores owned network settings before files can change.
CloseApplications=no
RestartApplications=no

[Languages]
Name: "chinesesimp"; MessagesFile: "ChineseSimplified.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"

[Files]
Source: "{#SourceRoot}\HQIP.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\desktop\assets\hqip.ico"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\HQIP-UI.exe"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\HQIP-UI.pck"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\hqip-runtime.exe"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\hqip-helper.exe"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\sing-box.exe"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\libcronet.dll"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\THIRD-PARTY-NOTICES.txt"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion
Source: "{#SourceRoot}\versions\bundled\sing-box-LICENSE"; DestDir: "{app}\versions\bundled"; Flags: ignoreversion

[Icons]
Name: "{group}\HQIP"; Filename: "{app}\HQIP.exe"; WorkingDir: "{app}"; IconFilename: "{app}\hqip.ico"
Name: "{autodesktop}\HQIP"; Filename: "{app}\HQIP.exe"; WorkingDir: "{app}"; IconFilename: "{app}\hqip.ico"; Tasks: desktopicon

[Run]
Filename: "{app}\HQIP.exe"; Description: "{cm:LaunchProgram,HQIP}"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; These directories contain application binaries only. Account data and update
; downloads under LocalAppData\HQIP\Godot are deliberately outside ownership.
Type: filesandordirs; Name: "{app}\versions"
Type: files; Name: "{app}\active.json"
Type: files; Name: "{app}\pending.json"

[Code]
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Command: String;
begin
  if CurUninstallStep = usUninstall then
    if RegQueryStringValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run', 'HQIP Godot', Command) then
      if CompareText(Command, '"' + ExpandConstant('{app}\HQIP.exe') + '"') = 0 then
        RegDeleteValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run', 'HQIP Godot');
end;
