rd /s /q build
mkdir build
#SET CGO_ENABLED=1
#SET GOARCH=amd64
#SET GOOS=windows
#go build
#move workdayAlarmClock.exe build\workdayAlarmClock.exe
#SET GOARCH=386
#go build
#move workdayAlarmClock.exe build\workdayAlarmClock-i386.exe
SET CGO_ENABLED=0
SET GOOS=linux
go build
move workdayAlarmClock build\workdayAlarmClock-linux-i386
SET GOARCH=amd64
go build
move workdayAlarmClock build\workdayAlarmClock-linux
SET GOARCH=arm
go build
copy workdayAlarmClock build\libWorkdayAlarmClock.so
move workdayAlarmClock build\workdayAlarmClock-linux-arm
#SET GOARCH=mips
#go build
#move workdayAlarmClock build\workdayAlarmClock-linux-mips
SET GOARCH=arm64
go build
move workdayAlarmClock build\workdayAlarmClock-linux-arm64
SET GOOS=darwin
go build
move workdayAlarmClock build\workdayAlarmClock-darwin-arm64
#SET GOARCH=amd64
#go build
#move workdayAlarmClock build\workdayAlarmClock-darwin
SET GOOS=freebsd
go build
move workdayAlarmClock build/workdayAlarmClock-freebsd