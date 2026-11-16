Unicode true
SetCompressor /SOLID lzma
SetCompressorDictSize 64

####
## 桌面工具箱 安装程序（由 `wails build -nsis` 调用，wails_tools.nsh 由 wails 自动生成）
## 额外安装 resources 目录（内置 Python 转换组件、FFmpeg 录屏组件）。
## 资源目录由 scripts/build.sh 准备到 build/stage/resources。
####
!define INFO_PROJECTNAME    "DesktopToolbox"
!define INFO_COMPANYNAME    "DesktopToolbox"
!define INFO_PRODUCTNAME    "桌面工具箱"
!define INFO_PRODUCTVERSION "0.2.0"
!define INFO_COPYRIGHT      "Copyright © 2026"
!define PRODUCT_EXECUTABLE  "DesktopToolbox.exe"
!define UNINST_KEY_NAME     "DesktopToolbox"
!define REQUEST_EXECUTION_LEVEL "admin"

!include "wails_tools.nsh"

VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} 安装程序"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

ManifestDPIAware true

!include "MUI2.nsh"
!include "LogicLib.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!define MUI_ABORTWARNING

!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "立即运行 桌面工具箱"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchAsUser

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"

Name "${INFO_PRODUCTNAME}"
Caption "${INFO_PRODUCTNAME} ${INFO_PRODUCTVERSION} 安装"
BrandingText "${INFO_PRODUCTNAME} ${INFO_PRODUCTVERSION}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe"
InstallDir "$PROGRAMFILES64\DesktopToolbox"
InstallDirRegKey HKLM "Software\DesktopToolbox" "InstallDir"
ShowInstDetails show
ShowUninstDetails show

Function .onInit
   !insertmacro wails.checkArchitecture
   ${IfNot} ${AtLeastWin10}
     MessageBox MB_OK|MB_ICONSTOP "桌面工具箱需要 Windows 10 或更高版本。"
     Abort
   ${EndIf}
FunctionEnd

; 以普通用户权限启动（经由资源管理器），避免程序以管理员身份运行导致无法从资源管理器拖放文件
Function LaunchAsUser
   Exec '"$WINDIR\explorer.exe" "$INSTDIR\${PRODUCT_EXECUTABLE}"'
FunctionEnd

!macro KillApp
    nsExec::Exec 'taskkill /F /IM ${PRODUCT_EXECUTABLE}'
    Sleep 600
!macroend

Section
    !insertmacro wails.setShellContext

    !insertmacro KillApp

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR
    !insertmacro wails.files

    ; 删除旧版本的组件，避免残留文件
    RMDir /r "$INSTDIR\resources"
    SetOutPath "$INSTDIR\resources"
    File /r "..\..\stage\resources\*.*"
    SetOutPath $INSTDIR

    SetRegView 64
    WriteRegStr HKLM "Software\DesktopToolbox" "InstallDir" "$INSTDIR"

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.writeUninstaller
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

    !insertmacro KillApp

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
    SetRegView 64
    DeleteRegKey HKLM "Software\DesktopToolbox"
    SetRegView 32
    DeleteRegKey HKLM "Software\DesktopToolbox"
    SetRegView 64

    ; 当前用户的数据
    SetShellVarContext current
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "DesktopToolbox"
    RMDir /r "$APPDATA\${PRODUCT_EXECUTABLE}" ; WebView2 缓存
    MessageBox MB_YESNO|MB_ICONQUESTION "是否同时删除剪贴板历史和设置？$\r$\n（位于 $APPDATA\DesktopToolbox）" /SD IDYES IDNO skipdata
        RMDir /r "$APPDATA\DesktopToolbox"
    skipdata:
    !insertmacro wails.setShellContext

    !insertmacro wails.deleteUninstaller
SectionEnd
