cd `dirname $0`
mkdir -p build
rm -rf build/*
# export CGO_ENABLED=1
# export GOARCH=amd64
# export GOOS=windows
# go build
# mv workdayAlarmClock.exe build/workdayAlarmClock.exe
export GOOS=linux
# go build
# mv workdayAlarmClock build/workdayAlarmClock-linux
export GOARCH=arm
go build
cp workdayAlarmClock build/libWorkdayAlarmClock.so
mv workdayAlarmClock build/workdayAlarmClock-linux-arm
# export GOARCH=mips
# go build
# mv workdayAlarmClock build/workdayAlarmClock-linux-mips
export GOARCH=arm64
go build
mv workdayAlarmClock build/workdayAlarmClock-linux-arm64
export GOOS=darwin
go build
mv workdayAlarmClock build/workdayAlarmClock-darwin-arm64
# export GOARCH=amd64
# go build
# mv workdayAlarmClock build/workdayAlarmClock-darwin