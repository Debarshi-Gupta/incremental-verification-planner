@rem Gradle startup script for Windows.
@rem Sample fixture wrapper: present for tool selection only; never executed by
@rem the fixture tests.
@echo off
set DIRNAME=%~dp0
if "%DIRNAME%" == "" set DIRNAME=.
set APP_HOME=%DIRNAME%
java -jar "%APP_HOME%gradle\wrapper\gradle-wrapper.jar" %*
