Knox Media — 文档转换引擎联调

支持引擎（按优先级）:
  1. Microsoft Office (COM, Windows)
  2. WPS Office (COM, Windows)
  3. LibreOffice (无头 soffice)

Windows 联调命令（在项目根目录）:

  go build -o tools/doctran/doctrans-test.exe ./cmd/doctrans-test/
  tools/doctran/doctrans-test.exe              # 检测全部引擎 + 样例转换
  tools/doctran/doctrans-test.exe -engine libreoffice
  tools/doctran/doctrans-test.exe -install-lo  # 一键安装/检测 LibreOffice

LibreOffice 便携版目录（自动检测）:
  tools/doctran/LibreOfficePortable/App/libreoffice/program/soffice.exe

系统选项 -> 文档转换：调整引擎优先级、检测状态、安装 LibreOffice。
