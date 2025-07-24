cd `dirname $0`
mkdir -p build
rm -rf build/*
# 设置交叉编译的环境变量
export GOOS=linux
export GOARCH=arm
export GOARM=7
export CGO_ENABLED=0
go build -ldflags="-w -s"
mv workdayAlarmClock build/workdayAlarmClock-arm
echo '编译完成'