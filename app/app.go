/*
 * App通信管道
 * zyyme 20250913
 * v1.0
 */

package app

import (
	"fmt"
	"log"
	"net"
	"time"
	"workdayAlarmClock/conf"

	"golang.org/x/net/ipv4"
)

var broadcastAddr *net.UDPAddr

func Init() {
	var err error
	broadcastAddr, err = getBroadcastAddress(25525)
	if err != nil {
		log.Println("警告: 无法获取广播地址，使用有限广播地址:", err)
		broadcastAddr = &net.UDPAddr{
			IP:   net.IPv4(255, 255, 255, 255),
			Port: 25525,
		}
	}
}

// 发送给app
func Send(s string) {
	if conf.Cfg.BroadcastMode {
		SendBroadcast(s)
	} else {
		fmt.Println(s)
	}
}

// 播放url
func PlayUrl(url string) {
	if conf.Cfg.BroadcastMode {
		Send("LOAD " + url)
		time.Sleep(time.Second * 3)
		Send("RESUME")
		time.Sleep(time.Second)
		Send("SEEK")
	} else {
		Send("PLAY " + url)
	}
}

// 发送广播
func SendBroadcast(s string) {
	if broadcastAddr == nil {
		Init()
	}

	// 设置UDP连接
	conn, err := net.DialUDP("udp4", nil, broadcastAddr)
	if err != nil {
		log.Println("无法创建UDP连接:", err)
	}
	defer conn.Close()

	// 允许广播
	pconn := ipv4.NewPacketConn(conn)
	pconn.SetTTL(1) // 限制在局域网内

	// 发送消息 发两次成功率更高
	_, _ = conn.Write([]byte(s))
	_, err = conn.Write([]byte(s))
	if err != nil {
		log.Println("发送失败:", err)
	}

	log.Println("已发送广播: ", s, "到", broadcastAddr.String())
}

// 获取本地ip
func GetLocalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "设备ip", err
	}

	for _, iface := range interfaces {
		// 检查接口状态：是否启动且非回环
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		// 获取接口上的地址列表
		addrs, err := iface.Addrs()
		if err != nil {
			log.Printf("Error getting addresses for interface %s: %v\n", iface.Name, err)
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			// 类型断言，处理 *net.IPNet 和 *net.IPAddr
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}

			// 跳过回环地址和IPv6地址
			if ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			// 跳过链路本地地址
			if ip.IsLinkLocalUnicast() {
				continue
			}

			return ip.String(), nil
		}
	}
	return "设备ip", fmt.Errorf("no suitable IPv4 address found")
}

// 获取广播地址
func getBroadcastAddress(port int) (*net.UDPAddr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range interfaces {
		// 只处理启用且非回环的接口
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}

			for _, addr := range addrs {
				// 检查是否为IP网络地址
				if ipnet, ok := addr.(*net.IPNet); ok {
					// 检查是否为IPv4地址
					if ipnet.IP.To4() != nil {
						// 计算广播地址
						ip := ipnet.IP.To4()
						mask := ipnet.Mask
						broadcast := make(net.IP, len(ip))
						for i := range ip {
							broadcast[i] = ip[i] | ^mask[i]
						}
						return &net.UDPAddr{
							IP:   broadcast,
							Port: port,
						}, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("未找到合适的网络接口")
}
