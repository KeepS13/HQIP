#ifndef SourceRoot
  #error SourceRoot is required
#endif
#ifndef OutputRoot
  #error OutputRoot is required
#endif
#ifndef ProductVersion
  #error ProductVersion is required
#endif
[Setup]
AppId={{87A84998-B48F-4B56-99C7-717315BFA38D}
AppName=HQIP 预览版
AppVersion={#ProductVersion}
AppPublisher=HQIP
DefaultDirName={localappdata}\Programs\HQIP Preview
DefaultGroupName=HQIP Preview
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
AppMutex=Local\HQIP.Flutter.Preview
CloseApplications=no
RestartApplications=no
OutputDir={#OutputRoot}
OutputBaseFilename=HQIP-Preview-Windows-x64-Setup
SetupIconFile={#SourceRoot}\versions\bundled\hqip.ico
UninstallDisplayIcon={app}\versions\bundled\hqip.ico
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ShowLanguageDialog=no
[Languages]
Name: "chinesesimp"; MessagesFile: "languages\ChineseSimplified.isl"
[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; Flags: unchecked
[Files]
Source: "{#SourceRoot}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
[Icons]
Name: "{group}\HQIP Preview"; Filename: "{app}\HQIP Preview.exe"; IconFilename: "{app}\versions\bundled\hqip.ico"
Name: "{autodesktop}\HQIP Preview"; Filename: "{app}\HQIP Preview.exe"; Tasks: desktopicon; IconFilename: "{app}\versions\bundled\hqip.ico"
[Run]
Filename: "{app}\HQIP Preview.exe"; Description: "{cm:LaunchProgram,HQIP 预览版}"; Flags: nowait postinstall skipifsilent
[InstallDelete]
Type: files; Name: "{app}\active.json"
Type: files; Name: "{app}\pending.json"
[UninstallDelete]
Type: filesandordirs; Name: "{app}\versions"
Type: files; Name: "{app}\active.json"
Type: files; Name: "{app}\pending.json"
[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "HQIP Preview"; Flags: deletevalue uninsdeletevalue
