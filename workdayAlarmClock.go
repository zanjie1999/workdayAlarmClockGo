/*
 * 工作日闹钟 Go
 * zyyme 20230630
 * v1.0
 */

package main

import "workdayAlarmClock/router"

func main() {
	router.Init("/wac").Run(":8080")
}
