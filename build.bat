@echo off

set AndroidProject=D:\AndroidProject\workdayAlarmClockAndroid

rd /s /q build
mkdir build
SET CGO_ENABLED=0

SET GOOS=android
SET GOARCH=arm64
mkdir %AndroidProject%\app\libs\arm64-v8a
go build -ldflags="-w -s" -o %AndroidProject%\app\libs\arm64-v8a\libWorkdayAlarmClock.so

SET GOOS=linux
SET GOARCH=arm
go build -ldflags="-w -s" -o build\workdayAlarmClock-linux-arm
mkdir %AndroidProject%\app\libs\armeabi-v7a
copy build\workdayAlarmClock-linux-arm %AndroidProject%\app\libs\armeabi-v7a\libWorkdayAlarmClock.so
SET GOARCH=386
go build -ldflags="-w -s" -o build\workdayAlarmClock-linux-i386
mkdir %AndroidProject%\app\libs\x86
copy build\workdayAlarmClock-linux-i386 %AndroidProject%\app\libs\x86\libWorkdayAlarmClock.so
SET GOARCH=amd64
go build -ldflags="-w -s" -o build\workdayAlarmClock-linux
SET GOARCH=mipsle
go build -ldflags="-w -s" -o build\workdayAlarmClock-linux-mipsle
SET GOARCH=arm64
go build -ldflags="-w -s" -o build\workdayAlarmClock-linux-arm64
SET GOOS=darwin
go build -ldflags="-w -s" -o build\workdayAlarmClock-darwin-arm64
SET GOARCH=amd64
go build -ldflags="-w -s" -o build\workdayAlarmClock-darwin

SET GOOS=windows
go build -ldflags="-w -s" -o build\workdayAlarmClock.exe
SET GOARCH=386
go build -ldflags="-w -s" -o build\workdayAlarmClock-i386.exe
