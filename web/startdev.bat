@echo off
cd /d "%~dp0"
echo 正在安装依赖...
call npm install
echo 安装完成，启动项目...
start /b npm run dev
timeout /t 3 /nobreak >nul
start msedge "http://localhost:5173"