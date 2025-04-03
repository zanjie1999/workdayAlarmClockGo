# 使用 Golang 重构的 工作咩闹钟  
原项目是 [工作日闹钟](https://github.com/zanjie1999/workdayAlarmClock)，从2017年用Python2写出来后，使用Python3重构，现在使用Golang重构，最大的原因是想适配Android  

本闹钟可以在设定的时间（支持中国法定节假日），从设定的网抑云歌单中随机抽取多首音乐作为闹钟铃声，可以自定义闹钟时长  
另外可以作为网抑云音乐播放器使用，随机播放永不重复，实现除语音助手外的智能音响应有的功能  

这是一个服务端程序，交互将通过8080端口的Web服务在浏览器完成，尽量减少ram占用，以便运行在骁龙210的随身WiFi上（包括Android端仅占用47M的Ram），使用蓝牙音响播放闹钟声音

这个程序将解决传统闹钟的几个问题：
1. 在节假日调休的情况下，该响的时候不响不该响的时候响
2. 闹钟铃声千篇一律，天天一样，容易听腻
3. 闹钟时间不够长，声音不够大，容易睡过头
4. 小爱音响断网后闹钟不会响
5. 闹钟随机音乐不能放我喜欢的歌
6. 随机播放重复概率过高

## 如何使用
Android使用 [App](https://github.com/zanjie1999/workdayAlarmClockAndroid)  

其他平台（Windows，Linux）推荐使用 [workdayAlarmClockGo](https://github.com/zanjie1999/meMp3Player) 作为播放器使用  
即这样启动 `workdayAlarmClock 你的播放器路径`  
比如 `workdayAlarmClock ./meMp3Player`

或者需要安装sox和curl，或者使用你喜欢的播放器  
Linux: `包管理器比如apt或者yum等 install sox curl`  
macOS: `brew install sox curl` 

Windows随便找个播放器基本都能用，需要播放时阻塞，放完自动退出的那种
Windows：这样启动 `workdayAlarmClock 你的播放器路径`  

暂停，音量控制目前仅在Android可用

打开同局域网任意设备的浏览器，访问 `http://你的设备ip地址:8080`  
点击 闹钟设置 根据说明进行设置  
对浏览器没有要求，ie5即使关闭js也能实现基础的功能  
请注意，当前尚不支持设置多个相同时间的闹钟，因为我没有这种需求

## 指令
除了直接在shell输入，还可以直接在访问地址后拼接，使用GET请求调用，如 `http://127.0.0.1:8080/1key`
```shell
# 停止播放
stop
# 下一首
next
# 上一首
prev
# 退出
exit
# 一键播放歌单、停止
1key
```

### 天气播报
会在每次闹钟停止后（手动停止或播放完自动停止），播报今天的天气和前一天的温度差，以便决定穿什么衣服  
你需要手动在闹钟设置中输入天气代码的框中输入你的区/市，并点击右边的查询按钮，保存设置后尝试点击“测试获取天气”来检查是否能正常使用  
因配额资源有限，请勿将我的语音合成api用于其他用途，谢谢合作，否则将会取消这一功能

