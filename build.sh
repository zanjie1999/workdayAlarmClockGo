AndroidProject='../../Android/StudioProjects/workdayAlarmClockAndroid'

cd `dirname $0`
mkdir -p build
rm -rf build/*
export CGO_ENABLED=0
if [ "${1:-}" != "1" ]; then
    export GOARCH=arm
    export GOOS=linux
    mkdir -p $AndroidProject/app/libs/armeabi
    go build -ldflags="-w -s" -o $AndroidProject/app/libs/armeabi/libWorkdayAlarmClock.so
    export GOARCH=386
    mkdir -p $AndroidProject/app/libs/x86
    go build -ldflags="-w -s" -o $AndroidProject/app/libs/x86/libWorkdayAlarmClock.so
    export GOOS=android
    export GOARCH=arm64
    mkdir -p $AndroidProject/app/libs/arm64-v8a
    go build -ldflags="-w -s" -o $AndroidProject/app/libs/arm64-v8a/libWorkdayAlarmClock.so
fi

export GOARCH=amd64
export GOOS=windows
go build -ldflags="-w -s"
mv workdayAlarmClock.exe build/workdayAlarmClock.exe
export GOARCH=386
go build -ldflags="-w -s" -o build/workdayAlarmClock-i386.exe

export GOOS=linux
go build -ldflags="-w -s" -o build/workdayAlarmClock-linux-i386
export GOARCH=amd64
go build -ldflags="-w -s" -o build/workdayAlarmClock-linux
export GOARCH=arm
go build -ldflags="-w -s" -o build/workdayAlarmClock-linux-arm
export GOARCH=mipsle
go build -ldflags="-w -s" -o build/workdayAlarmClock-linux-mipsle
export GOARCH=arm64
go build -ldflags="-w -s" -o build/workdayAlarmClock-linux-arm64
export GOOS=darwin
go build -ldflags="-w -s" -o build/workdayAlarmClock-darwin-arm64
export GOARCH=amd64
go build -ldflags="-w -s" -o build/workdayAlarmClock-darwin