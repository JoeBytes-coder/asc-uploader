; Inno Setup 脚本（可选）：把 asc-uploader-ui.exe 打成标准 Windows 安装包 setup.exe
; 用法：安装 Inno Setup 6（https://jrsoftware.org/isinfo.php）后，
;      在本目录执行：iscc asc-uploader-ui.iss
; 产物：dist/asc-uploader-ui-setup.exe

#define MyAppName "asc-uploader-ui"
#define MyAppVersion "4.0.0"
#define MyAppPublisher "asc-uploader"
#define MyAppExeName "asc-uploader-ui.exe"

[Setup]
AppId={{8B3D2E1A-6C7F-4A9E-B2D4-ascupl00der1}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
UninstallDisplayIcon={app}\{#MyAppExeName}
OutputDir=dist
OutputBaseFilename=asc-uploader-ui-setup
Compression=lzma2/max
SolidCompression=yes
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=lowest
WizardStyle=modern

[Languages]
Name: "chinesesimplified"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"

[Files]
Source: "..\build\bin\asc-uploader-ui.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{group}\卸载 {#MyAppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "立即运行 {#MyAppName}"; Flags: nowait postinstall skipifsilent
