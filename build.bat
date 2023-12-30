
set AndroidProject=../../StudioProjects/workdayAlarmClockAndroid

rd /s /q build
mkdir build
#SET CGO_ENABLED=1
SET GOARCH=amd64
SET GOOS=windows
go build -ldflags="-w -s"
move workdayAlarmClock.exe build\workdayAlarmClock.exe
SET GOARCH=386
go build -ldflags="-w -s"
move workdayAlarmClock.exe build\workdayAlarmClock-i386.exe
SET CGO_ENABLED=0
SET GOOS=linux
go build -ldflags="-w -s"
move workdayAlarmClock build\workdayAlarmClock-linux-i386
SET GOARCH=amd64
go build -ldflags="-w -s"
move workdayAlarmClock build\workdayAlarmClock-linux
SET GOARCH=arm
go build -ldflags="-w -s"
mkdir %AndroidProject%/app/libs/armeabi
copy workdayAlarmClock %AndroidProject%/app/libs/armeabi/libWorkdayAlarmClock.so
move workdayAlarmClock build\workdayAlarmClock-linux-arm
SET GOARCH=mips
go build -ldflags="-w -s"
move workdayAlarmClock build\workdayAlarmClock-linux-mips
SET GOARCH=arm64
go build -ldflags="-w -s"
mkdir %AndroidProject%/app/libs/arm64-v8a
copy workdayAlarmClock %AndroidProject%/app/libs/arm64-v8a/libWorkdayAlarmClock.so
move workdayAlarmClock build\workdayAlarmClock-linux-arm64
SET GOOS=darwin
go build -ldflags="-w -s"
move workdayAlarmClock build\workdayAlarmClock-darwin-arm64
SET GOARCH=amd64
go build -ldflags="-w -s"
move workdayAlarmClock build\workdayAlarmClock-darwin