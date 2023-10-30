AndroidProject='../../Android/StudioProjects/workdayAlarmClockAndroid'

cd `dirname $0`
mkdir -p build
rm -rf build/*
# export CGO_ENABLED=1
# export GOARCH=amd64
# export GOOS=windows
# go build -ldflags="-w -s"
# mv workdayAlarmClock.exe build/workdayAlarmClock.exe
export GOOS=linux
go build -ldflags="-w -s"
mv workdayAlarmClock build/workdayAlarmClock-linux
export GOARCH=arm
go build -ldflags="-w -s"
mkdir -p $AndroidProject/app/libs/armeabi
cp workdayAlarmClock $AndroidProject/app/libs/armeabi/libWorkdayAlarmClock.so
mv workdayAlarmClock build/workdayAlarmClock-linux-arm
# export GOARCH=mips
# go build -ldflags="-w -s"
# mv workdayAlarmClock build/workdayAlarmClock-linux-mips
export GOARCH=arm64
go build -ldflags="-w -s"
mkdir -p $AndroidProject/app/libs/arm64-v8a
cp workdayAlarmClock $AndroidProject/app/libs/arm64-v8a/libWorkdayAlarmClock.so
mv workdayAlarmClock build/workdayAlarmClock-linux-arm64
export GOOS=darwin
go build -ldflags="-w -s"
mv workdayAlarmClock build/workdayAlarmClock-darwin-arm64
# export GOARCH=amd64
# go build -ldflags="-w -s"
# mv workdayAlarmClock build/workdayAlarmClock-darwin