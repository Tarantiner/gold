// author: tarantiner@163.com

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"fyne.io/fyne/v2/theme"
	"image/color"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/sys/windows"
)

type Quote struct {
	Data []struct {
		QuoteData struct {
			Q63 string `json:"q63"`
			Q67 string `json:"q67"`
		} `json:"quote"`
	} `json:"data"`
}

type CustomTheme struct {
	fyne.Theme
}

func (c *CustomTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if name == theme.ColorNameDisabled {
		return color.White // 强制输入文本为黑色
	}
	// 其他颜色走默认主题
	return c.Theme.Color(name, variant)
}

var maxLogLines int
var notify bool
var key string

func init() {
	flag.IntVar(&maxLogLines, "n", 1000, "显示多少行日志")
	flag.BoolVar(&notify, "notify", false, "是否通知")
	flag.StringVar(&key, "k", "SCT291613TsbPfeE1oOFP9BT5cQIhHoYZA", "显示通知所用key，参考")
	flag.Parse()
}

func showAlertPopup(message string) {
	msg, _ := windows.UTF16PtrFromString("提醒")
	content, _ := windows.UTF16PtrFromString(message)
	windows.MessageBox(0, content, msg, windows.MB_ICONINFORMATION)
	//os.Exit(1)
}

func scSend(sendkey, title, desp string) (map[string]interface{}, error) {
	var url string
	if strings.HasPrefix(sendkey, "sctp") {
		parts := strings.SplitN(sendkey, "t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("通知无效的 sendkey 格式: %s", sendkey)
		}
		num := strings.TrimPrefix(parts[0], "sctp")
		url = fmt.Sprintf("https://%s.push.ft07.com/send/%s.send", num, sendkey)
	} else {
		url = fmt.Sprintf("https://sctapi.ftqq.com/%s.send", sendkey)
	}

	payload := map[string]string{
		"title": title,
		"desp":  desp,
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("通知JSON 序列化失败: %v", err)
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonData)))
	if err != nil {
		return nil, fmt.Errorf("通知创建请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json;charset=utf-8")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("通知发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("通知读取响应失败: %v", err)
	}

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}
	return result, nil
}

func main() {
	// 初始化 Fyne 应用
	myApp := app.New()
	myApp.Settings().SetTheme(&CustomTheme{Theme: theme.DarkTheme()})
	myWindow := myApp.NewWindow("黄金价格监控")
	//myWindow.SetIcon(resourceIconPng)
	myWindow.Resize(fyne.NewSize(600, 520))

	// 输入字段
	buyPriceEntry := widget.NewEntry()
	buyPriceEntry.SetPlaceHolder("请输入买入价格（如 935.5）")
	targetPriceEntry := widget.NewEntry()
	targetPriceEntry.SetPlaceHolder("请输入目标价格（如 970.0）")
	intervalEntry := widget.NewEntry()
	intervalEntry.SetPlaceHolder("请输入间隔时间（秒，如 10）")

	// 日志区域
	logText := widget.NewMultiLineEntry()
	logText.Disable()
	logText.SetMinRowsVisible(15) // 默认显示 15 行
	logScroll := container.NewVScroll(logText)
	logScroll.SetMinSize(fyne.NewSize(400, 300)) // 设置日志区域最小高度 300 像素

	// 运行/暂停按钮
	runButton := widget.NewButton("运行", nil)
	isRunning := false
	var logMutex sync.Mutex
	var buttonMutex sync.Mutex
	var stopChan chan struct{}
	var errList []int
	var logLines []string

	// 追加日志并带时间戳
	log := func(msg string) {
		logMutex.Lock()
		defer logMutex.Unlock()

		line := time.Now().Format("2006-01-02 15:04:05") + ": " + msg
		logLines = append(logLines, line)

		if len(logLines) > maxLogLines {
			logLines = logLines[len(logLines)-maxLogLines:]
		}

		var builder strings.Builder
		for _, l := range logLines {
			builder.WriteString(l)
			builder.WriteByte('\n')
		}

		finalText := builder.String()

		fyne.Do(func() {
			logText.SetText(finalText)
			logText.Refresh()
			time.AfterFunc(50*time.Millisecond, func() {
				fyne.Do(func() {
					logScroll.ScrollToBottom()
				})
			})
			//logScroll.ScrollToBottom()
		})
	}

	// HTTP 请求函数
	fetchPrice := func() (float64, error) {
		client := &http.Client{}
		req, err := http.NewRequest("GET", "https://api.jijinhao.com/realtime/quotejs.htm", nil)
		if err != nil {
			return 0, err
		}

		// 设置请求头
		req.Header.Set("Authority", "api.jijinhao.com")
		req.Header.Set("Pragma", "no-cache")
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/83.0.4103.97 Safari/537.36")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "no-cors")
		req.Header.Set("Sec-Fetch-Dest", "script")
		req.Header.Set("Referer", "https://www.cngold.org/paper/gonghang.html")
		req.Header.Set("Accept-Language", "en-GB,en-US;q=0.9,en;q=0.8,zh-CN;q=0.7,zh;q=0.6")

		// 设置查询参数
		q := req.URL.Query()
		q.Add("categoryId", "225")
		q.Add("currentPage", "1")
		q.Add("pageSize", "8")
		q.Add("_", strconv.FormatInt(time.Now().UnixMilli(), 10))
		req.URL.RawQuery = q.Encode()

		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, err
		}

		// 解析 JSON
		re := regexp.MustCompile(`quot_str = \[(.+)\]`)
		matches := re.FindStringSubmatch(string(body))
		if len(matches) < 2 {
			return 0, fmt.Errorf("无法解析 quot_str")
		}

		var quotes Quote
		err = json.Unmarshal([]byte(matches[1]), &quotes)
		if err != nil {
			return 0, err
		}

		for _, item := range quotes.Data {
			if item.QuoteData.Q67 == "工行积存金" {
				price, err := strconv.ParseFloat(item.QuoteData.Q63, 64)
				if err != nil {
					return 0, err
				}
				return price, nil
			}
		}
		return 0, fmt.Errorf("未找到工行积存金")
	}

	// 主循环
	go func() {
		for {
			select {
			case <-stopChan:
				return
			default:
				if !isRunning {
					time.Sleep(100 * time.Millisecond)
					continue
				}

				// 验证输入
				buyPrice, err := strconv.ParseFloat(buyPriceEntry.Text, 64)
				if err != nil {
					log("买入价格无效")
					continue
				}
				targetPrice, err := strconv.ParseFloat(targetPriceEntry.Text, 64)
				if err != nil {
					log("目标价格无效")
					continue
				}
				interval, err := strconv.Atoi(intervalEntry.Text)
				if err != nil || interval <= 0 {
					log("间隔时间无效")
					continue
				}

				// 获取价格
				price, err := fetchPrice()
				if err != nil {
					log(fmt.Sprintf("错误: %v", err))
					errList = append(errList, 1)
					if len(errList) > 5 {
						errList = errList[len(errList)-5:]
					}
					if len(errList) == 5 && sum(errList) == 5 {
						log("多次错误，停止运行")
						buttonMutex.Lock()
						isRunning = false
						fyne.Do(func() {
							runButton.SetText("运行")
						})
						buttonMutex.Unlock()
						errList = nil
						break
					}
				} else {
					log(fmt.Sprintf("当前价格: %.2f", price))
					errList = append(errList, 0)
					if len(errList) > 5 {
						errList = errList[len(errList)-5:]
					}
					if price >= targetPrice {
						msg := fmt.Sprintf("\n买入价格: %.2f\n当前价格: %.2f\n目标价格%.2f\n可以卖出！", buyPrice, price, targetPrice)
						log(msg)
						if notify && key != "" {
							_, err = scSend(key, "卖出提醒", msg)
							if err != nil {
								log(fmt.Sprintf("微信通知失败：【%s】", err.Error()))
							}
						}
						showAlertPopup(msg)
						//dialog.ShowInformation("目标达成", fmt.Sprintf("\n买入价格: %.2f\n当前价格: %.2f\n目标价格%.2f\n可以卖出！", buyPrice, price, targetPrice), myWindow)
						buttonMutex.Lock()
						isRunning = false
						fyne.Do(func() {
							runButton.SetText("运行")
						})
						buttonMutex.Unlock()
						break
					}
				}
				time.Sleep(time.Duration(interval) * time.Second)
			}
		}
	}()

	// 按钮动作
	runButton.OnTapped = func() {
		buttonMutex.Lock()
		defer buttonMutex.Unlock()
		if isRunning {
			isRunning = false
			runButton.SetText("运行")
			log("已暂停")
		} else {
			if buyPriceEntry.Text == "" || targetPriceEntry.Text == "" || intervalEntry.Text == "" {
				go func() {
					log("请填写所有字段")
				}()
				return
			}
			isRunning = true
			stopChan = make(chan struct{})
			runButton.SetText("暂停")
			log("已启动")
		}
	}

	// 布局
	form := container.New(layout.NewFormLayout(),
		widget.NewLabel("买入价格："), buyPriceEntry,
		widget.NewLabel("目标价格："), targetPriceEntry,
		widget.NewLabel("间隔时间（秒）："), intervalEntry,
	)
	content := container.NewVBox(
		form,
		runButton,
		widget.NewLabel("日志："),
		logScroll,
	)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

// 辅助函数：计算切片总和
func sum(arr []int) int {
	total := 0
	for _, v := range arr {
		total += v
	}
	return total
}
