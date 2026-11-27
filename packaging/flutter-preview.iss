#ifndef SourceRoot
  #error SourceRoot is required
#endif
#ifndef OutputRoot
  #error OutputRoot is required
#endif
#ifndef ProductVersion
  #error ProductVersion is required
#endif
#ifndef ProductSequence
  #error ProductSequence is required
#endif
[Setup]
AppId={{87A84998-B48F-4B56-99C7-717315BFA38D}
AppName=HQIP 预览版
AppVersion={#ProductVersion}
AppPublisher=HQIP
DefaultDirName={localappdata}\Programs\HQIP Preview
DefaultGroupName=HQIP Preview
UsePreviousAppDir=yes
DisableDirPage=no
DisableProgramGroupPage=yes
DisableWelcomePage=no
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
AppMutex=Local\HQIP.Flutter.Preview
SetupMutex=Local\HQIP.Flutter.Preview.Setup
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
; Inspect with this installer's trusted launcher, never an installed executable.
Source: "{#SourceRoot}\HQIP Preview.exe"; DestName: "hqip-preview-installer-probe.exe"; Flags: dontcopy
Source: "{#SourceRoot}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
[Icons]
Name: "{group}\HQIP Preview"; Filename: "{app}\HQIP Preview.exe"; IconFilename: "{app}\versions\bundled\hqip.ico"
Name: "{autodesktop}\HQIP Preview"; Filename: "{app}\HQIP Preview.exe"; Tasks: desktopicon; IconFilename: "{app}\versions\bundled\hqip.ico"
[Run]
Filename: "{app}\HQIP Preview.exe"; Description: "{cm:LaunchProgram,HQIP 预览版}"; Flags: nowait postinstall skipifsilent
; Keep authenticated hot-update pointers. The launcher ignores sequences older
; than its bundled payload; installation must never erase a newer update.
[UninstallDelete]
Type: filesandordirs; Name: "{app}\versions"
Type: files; Name: "{app}\active.json"
Type: files; Name: "{app}\pending.json"
[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "HQIP Preview"; Flags: uninsdeletevalue

[Messages]
SetupAppRunningError=安装程序检测到 HQIP 预览版仍在运行。%n%n请从系统托盘菜单选择“退出”，等待连接断开后继续。关闭窗口只会隐藏界面。

[Code]
var
  DetectedDirectory, DetectedVersion, InstallMode, InspectionError: String;

function InspectInstallation(const Candidate: String): Boolean;
var
  ReportPath, ProbePath, Parameters: String;
  ExitCode: Integer;
begin
  ReportPath := ExpandConstant('{tmp}\preview-install.ini');
  ProbePath := ExpandConstant('{tmp}\hqip-preview-installer-probe.exe');
  DeleteFile(ReportPath);
  Parameters := '--installer-inspect ' + AddQuotes(Candidate) + ' ' + AddQuotes(ReportPath);
  Result := Exec(ProbePath, Parameters, '', SW_HIDE, ewWaitUntilTerminated, ExitCode);
  if Result then Result := (ExitCode = 0) and FileExists(ReportPath);
  InspectionError := '无法检查现有预览版的安装状态，安装尚未开始。请重试或联系支持。';
  if not Result then Exit;
  DetectedDirectory := GetIniString('Install', 'Directory', '', ReportPath);
  DetectedVersion := GetIniString('Install', 'Version', '', ReportPath);
  InstallMode := GetIniString('Install', 'Mode', '', ReportPath);
  if (InstallMode = 'blocked') or (InstallMode = 'downgrade') then begin
    InspectionError := GetIniString('Install', 'Message', InspectionError, ReportPath);
    Result := False;
  end else
    Result := (DetectedDirectory <> '') and
      ((InstallMode = 'install') or (InstallMode = 'upgrade') or (InstallMode = 'repair'));
end;

function InitializeSetup: Boolean;
begin
  ExtractTemporaryFile('hqip-preview-installer-probe.exe');
  Result := InspectInstallation(ExpandConstant('{localappdata}\Programs\HQIP Preview'));
  if not Result then
    SuppressibleMsgBox(InspectionError, mbError, MB_OK, IDOK);
end;

function InstallAction: String;
begin
  if InstallMode = 'repair' then Result := '修复 / 重新安装'
  else if InstallMode = 'upgrade' then Result := '升级'
  else Result := '安装';
end;

procedure InitializeWizard;
begin
  if InstallMode <> 'install' then begin
    WizardForm.DirEdit.Text := DetectedDirectory;
    WizardForm.DirEdit.Enabled := False;
    WizardForm.DirBrowseButton.Enabled := False;
    WizardForm.WelcomeLabel2.Caption :=
      '已检测到 HQIP 预览版 ' + DetectedVersion + '。' + #13#10#13#10 +
      '本次操作：' + InstallAction + #13#10 +
      '安装包版本：{#ProductVersion}（序号 {#ProductSequence}）' + #13#10#13#10 +
      '将继续使用原安装目录：' + #13#10 + DetectedDirectory + #13#10#13#10 +
      '账户、订阅与设置会保留。安装前请从托盘菜单退出客户端。';
    WizardForm.SelectDirLabel.Caption :=
      '已检测到现有预览版，将在原目录中' + InstallAction + '，避免创建重复安装。';
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (CurPageID = wpSelectDir) and (InstallMode <> 'install') then begin
    WizardForm.PageNameLabel.Caption := '确认' + InstallAction + '位置';
    WizardForm.PageDescriptionLabel.Caption := '当前版本：' + DetectedVersion +
      '    安装包：{#ProductVersion}';
  end;
  if CurPageID = wpReady then
    WizardForm.NextButton.Caption := InstallAction + '(&I)';
end;

function UpdateReadyMemo(Space, NewLine, MemoUserInfoInfo, MemoDirInfo, MemoTypeInfo,
  MemoComponentsInfo, MemoGroupInfo, MemoTasksInfo: String): String;
begin
  Result := '操作：' + InstallAction + NewLine;
  if DetectedVersion <> '' then
    Result := Result + '当前版本：' + DetectedVersion + NewLine;
  Result := Result + '安装包版本：{#ProductVersion}（序号 {#ProductSequence}）' +
    NewLine + NewLine + MemoDirInfo + NewLine + NewLine +
    '账户、订阅、设置与已下载的更新保持保留。';
  if MemoTasksInfo <> '' then Result := Result + NewLine + NewLine + MemoTasksInfo;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  SelectedDirectory: String;
begin
  Result := '';
  if CheckForMutexes('Local\HQIP.Flutter.Preview') then begin
    Result := 'HQIP 预览版仍在运行。请从系统托盘菜单选择“退出”，等待连接断开后重试。';
    Exit;
  end;
  SelectedDirectory := WizardDirValue;
  { Re-check immediately before any file is written: another update could have
    finished while the wizard was open, or a custom destination was selected. }
  if not InspectInstallation(SelectedDirectory) then begin
    Result := InspectionError;
    Exit;
  end;
  if CompareText(RemoveBackslashUnlessRoot(SelectedDirectory),
    RemoveBackslashUnlessRoot(DetectedDirectory)) <> 0 then
    Result := '检测到现有预览版的安装目录发生变化。请重新打开安装程序以继续升级。';
end;
