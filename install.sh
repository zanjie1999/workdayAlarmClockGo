# bin.bash
cp ./build/workdayAlarmClock-arm /opt/aku/clock/workdayAlarmClock-arm
cp ./install/akuclock.service /usr/lib/systemd/system/akuclock.service
systemctl daemon-reload
systemctl enable akuclock
systemctl start akuclock
