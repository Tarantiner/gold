// author: tarantiner@163.com
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/sys/windows"

	"gopkg.in/ini.v1"      // INI 解析
	_ "modernc.org/sqlite" // SQLite 驱动（纯 Go）
)

type Quote struct {
	Data []struct {
		QuoteData struct {
			Q63 string `json:"q63"`
			Q67 string `json:"q67"`
		} `json:"quote"`
	} `json:"data"`
}

type PriceInfo struct {
	T     int64
	Price float64
}

/* ---------- 配置 ---------- */
type Config struct {
	MaxLogLines int
	Notify      bool
	Key         string
	SqlitePath  string
}

var cfg Config

func loadConfig() error {
	// 默认值
	cfg = Config{
		MaxLogLines: 1000,
		Notify:      false,
		Key:         "SCT291613TsbPfeE1oOFP9BT5cQIhHoYZA",
		SqlitePath:  "./gold_price.db",
	}

	// 读取 conf.ini
	iniFile, err := ini.Load("conf.ini")
	if err != nil {
		return nil // 忽略错误，使用默认值
	}

	sec := iniFile.Section("")
	if v := sec.Key("max_log_lines").String(); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.MaxLogLines = i
		}
	}
	if v := sec.Key("notify").String(); v != "" {
		cfg.Notify = strings.ToLower(v) == "true" || v == "1"
	}
	if v := sec.Key("key").String(); v != "" {
		cfg.Key = v
	}
	if v := sec.Key("sqlite_path").String(); v != "" {
		cfg.SqlitePath = v
	}
	return nil
}

/* ---------- SQLite 初始化 ---------- */
var dbMutex sync.Mutex
var db *sql.DB
var insertStmt *sql.Stmt
var recLis []*PriceInfo

func initDB() error {
	var err error
	dbMutex.Lock()
	defer dbMutex.Unlock()

	// 打开数据库
	db, err = sql.Open("sqlite", cfg.SqlitePath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout=5000")
	if err != nil {
		return err
	}

	// 创建表
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS price_log (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            ts TEXT NOT NULL,
            price REAL NOT NULL
        );
    `)
	if err != nil {
		return err
	}

	cutoff := time.Now().AddDate(-1, 0, 0).Format(time.RFC3339) // 一年前

	_, err = db.Exec(`DELETE FROM price_log WHERE ts < ?`, cutoff)
	if err != nil {
		return err
	}

	//rowsAffected, _ := result.RowsAffected()
	//fmt.Printf("已删除 %d 条一年前的记录\n", rowsAffected)

	// 准备插入语句
	insertStmt, err = db.Prepare("INSERT INTO price_log(ts, price) VALUES(?, ?)")
	if err != nil {
		return err
	}

	// 全局保存 db 连接（这里简化为全局 insertStmt）
	// 注意：这里不关闭 db，程序运行期间保持连接
	return nil
}

// 查询最近12小时的数据
func getRecentPriceData() []*PriceInfo {
	dbMutex.Lock()
	defer dbMutex.Unlock()
	if insertStmt == nil {
		return []*PriceInfo{}
	}
	// 计算12小时前的时间点（RFC3339格式）
	longTimeAgo := time.Now().Add(-12 * time.Hour).Format(time.RFC3339)

	query := `
        SELECT ts, price 
        FROM price_log 
        WHERE ts >= ? 
        ORDER BY ts ASC
    `

	rows, err := db.Query(query, longTimeAgo)
	if err != nil {
		return []*PriceInfo{}
	}
	defer rows.Close()

	var priceData []*PriceInfo

	for rows.Next() {
		var tsStr string
		var price float64
		var priceInfo PriceInfo

		err := rows.Scan(&tsStr, &price)
		if err != nil {
			return []*PriceInfo{}
		}

		// 将RFC3339时间字符串转换为time.Time
		t, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			return []*PriceInfo{}
		}

		// 转换为Unix时间戳（秒）
		priceInfo.T = t.Unix()
		priceInfo.Price = price

		priceData = append(priceData, &priceInfo)
	}

	if err = rows.Err(); err != nil {
		return []*PriceInfo{}
	}

	return priceData
}

// 记录价格到 SQLite（仅 interval == 15）
func logPriceToDB(price float64) {
	dbMutex.Lock()
	defer dbMutex.Unlock()

	if insertStmt == nil {
		return
	}

	ts := time.Now().Format(time.RFC3339)
	_, err := insertStmt.Exec(ts, price)
	if err != nil {
		// 记录错误但不中断主流程
		fmt.Printf("SQLite 写入失败: %v\n", err)
	}
}

/* ---------- 全局变量（从配置读取） ---------- */
var maxLogLines int
var notify bool
var key string

func init() {
	_ = loadConfig() // 加载配置
	maxLogLines = cfg.MaxLogLines
	notify = cfg.Notify
	key = cfg.Key

	// 保留 flag 覆盖能力
	flag.IntVar(&maxLogLines, "n", maxLogLines, "显示多少行日志")
	flag.BoolVar(&notify, "notify", notify, "是否通知")
	flag.StringVar(&key, "k", key, "显示通知所用key，参考")
	flag.Parse()
}

func showAlertPopup(message string) {
	// 使用简单可靠的方法创建置顶弹窗
	msg, _ := windows.UTF16PtrFromString("黄金价格提醒")
	content, _ := windows.UTF16PtrFromString(message)
	// 使用MB_TOPMOST确保置顶，MB_SETFOREGROUND确保获得焦点
	flags := uint32(windows.MB_ICONINFORMATION | windows.MB_OK | windows.MB_SETFOREGROUND | windows.MB_TOPMOST)
	// 单次显示，确保置顶
	windows.MessageBox(0, content, msg, flags)
}

//func showAlertPopup(message string) {
//	// 使用简单可靠的方法创建置顶弹窗
//	msg, _ := windows.UTF16PtrFromString("黄金价格提醒")
//	content, _ := windows.UTF16PtrFromString(message)
//	// 使用MB_TOPMOST确保置顶，MB_SETFOREGROUND确保获得焦点
//	flags := uint32(windows.MB_ICONINFORMATION | windows.MB_OK | windows.MB_SETFOREGROUND)
//	// 第一次尝试：使用默认窗口
//	windows.MessageBox(0, content, msg, flags)
//	// 短暂延迟后再次尝试，确保置顶
//	time.Sleep(100 * time.Millisecond)
//	// 第二次尝试：添加MB_TOPMOST
//	windows.MessageBox(0, content, msg, flags|windows.MB_TOPMOST)
//}

func scSend(sendkey, title, desp string) (map[string]interface{}, error) {
	var url string
	if strings.HasPrefix(sendkey, "sctp") {
		parts := strings.SplitN(sendkey, "t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("无效的 sendkey 格式: %s", sendkey)
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
	jsonData, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, strings.NewReader(string(jsonData)))
	req.Header.Set("Content-Type", "application/json;charset=utf-8")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	_ = json.Unmarshal(body, &result)
	return result, nil
}

func getMedian(nums []float64) float64 {
	if len(nums) == 0 {
		return 0
	}

	// 复制一份，避免修改原始切片
	data := make([]float64, len(nums))
	copy(data, nums)
	sort.Float64s(data)

	n := len(data)
	if n%2 == 1 {
		// 奇数：直接取中间
		return data[n/2]
	}

	// 偶数：取中间两个数的平均值
	mid1 := data[n/2-1]
	mid2 := data[n/2]
	return (mid1 + mid2) / 2
}

func getStatsPrice(recLis []*PriceInfo, n int64) (maxVal, minVal, avgVal, medVal float64) {
	// 初始化最大值、最小值和总和
	maxVal = 0
	minVal = 0.0
	sum := 0.0

	// 遍历切片计算
	nowTime := time.Now().Unix()
	useN := 0
	//fmt.Printf("计算%d分钟内\n", n)
	//fmt.Println("长度", len(recLis))
	//var j int
	priceList := make([]float64, 0, 120)
	for _, itm := range recLis {
		if itm.T+n*60 < nowTime {
			//j = i
			continue
		}
		useN++
		priceList = append(priceList, itm.Price)
		if maxVal == 0.0 || itm.Price > maxVal {
			maxVal = itm.Price
		}
		if minVal == 0.0 || itm.Price < minVal {
			minVal = itm.Price
		}
		sum += itm.Price
	}
	//recLis = recLis[j+1 : len(recLis)]

	// 计算平均值
	avgVal = sum / float64(useN)
	medVal = getMedian(priceList)
	return
}

func main() {
	// 初始化 SQLite
	if err := initDB(); err != nil {
		panic("SQLite 初始化失败: " + err.Error())
	}
	recLis = getRecentPriceData()
	//fmt.Printf("获取到%d条记录\n", len(recLis))
	//fmt.Println((*recLis[0]).T, (*recLis[0]).Price)
	//fmt.Println((*recLis[len(recLis)-1]).T, (*recLis[len(recLis)-1]).Price)

	// Fyne UI
	myApp := app.New()
	myApp.Settings().SetTheme(theme.DarkTheme())
	myWindow := myApp.NewWindow("黄金价格监控")
	myWindow.SetIcon(resourceIconPng)
	//myWindow.Resize(fyne.NewSize(720, 540))
	myWindow.Resize(fyne.NewSize(720, 0))

	// 输入框
	buyPriceEntry := widget.NewEntry()
	buyPriceEntry.SetPlaceHolder("请输入买入价格（如 935.5）")
	targetBuyPriceEntry := widget.NewEntry()
	targetBuyPriceEntry.SetPlaceHolder("请输入目标买入价格（如 900.0）")
	targetSellPriceEntry := widget.NewEntry()
	targetSellPriceEntry.SetPlaceHolder("请输入目标卖出价格（如 970.0）")
	intervalEntry := widget.NewEntry()
	intervalEntry.SetPlaceHolder("请输入间隔时间（秒，如 10）")
	statsEntry := widget.NewEntry()
	statsEntry.SetPlaceHolder("请输入统计时间（分钟，如 10）")
	profitEntry := widget.NewEntry()
	currEntry := widget.NewEntry()

	// 通知开关
	notifyCheck := widget.NewCheck("启用通知提醒", func(checked bool) {
		notify = checked
	})

	// 日志区
	logText := widget.NewLabel("")
	logText.Wrapping = fyne.TextWrapWord
	logScroll := container.NewVScroll(logText)
	logScroll.SetMinSize(fyne.NewSize(400, 150)) // 减小最小高度，便于用户缩小窗口

	// 运行按钮
	runButton := widget.NewButton("运行", nil)
	isRunning := false
	var logMutex sync.Mutex
	var buttonMutex sync.Mutex
	var stopChan chan struct{}
	var errList []int
	logLines := make([]string, 0, maxLogLines+2)

	// 日志函数
	log := func(msg string) {
		logMutex.Lock()
		line := time.Now().Format("2006-01-02 15:04:05") + ": " + msg
		logLines = append(logLines, line)
		if len(logLines) > maxLogLines {
			logLines = logLines[len(logLines)-maxLogLines:]
		}
		displayText := strings.Join(logLines, "\n")
		fyne.Do(func() {
			logText.SetText(displayText)
			logText.Refresh()
			time.AfterFunc(50*time.Millisecond, func() {
				fyne.Do(func() { logScroll.ScrollToBottom() })
			})
		})
		logMutex.Unlock()
	}

	// 获取价格
	fetchPrice := func() (float64, error) {
		client := &http.Client{}
		req, _ := http.NewRequest("GET", "https://api.jijinhao.com/realtime/quotejs.htm", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Referer", "https://www.cngold.org/paper/gonghang.html")
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
		body, _ := io.ReadAll(resp.Body)

		re := regexp.MustCompile(`quot_str = \[(.+)\]`)
		matches := re.FindStringSubmatch(string(body))
		if len(matches) < 2 {
			return 0, fmt.Errorf("无法解析 quot_str")
		}

		var quotes Quote
		if err := json.Unmarshal([]byte(matches[1]), &quotes); err != nil {
			return 0, err
		}

		for _, item := range quotes.Data {
			if item.QuoteData.Q67 == "工行积存金" {
				price, _ := strconv.ParseFloat(item.QuoteData.Q63, 64)
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

				// 解析输入
				buyPrice, _ := strconv.ParseFloat(buyPriceEntry.Text, 64)
				targetBuyPrice, _ := strconv.ParseFloat(targetBuyPriceEntry.Text, 64)
				targetSellPrice, _ := strconv.ParseFloat(targetSellPriceEntry.Text, 64)
				interval, err := strconv.Atoi(intervalEntry.Text)
				if err != nil || interval <= 0 {
					log("间隔时间无效")
					time.Sleep(1 * time.Second)
					continue
				}

				statsNum, err := strconv.Atoi(statsEntry.Text)
				if err != nil || statsNum <= 0 {
					log("统计时间无效")
				}

				price, err := fetchPrice()
				if err != nil {
					log(fmt.Sprintf("错误: %v", err))
					errList = append(errList, 1)
					if len(errList) > 5 {
						errList = errList[len(errList)-5:]
					}
					if len(errList) == 5 && sum(errList) == 5 {
						log("连续5次错误，停止运行")
						isRunning = false
						fyne.Do(func() { runButton.SetText("运行") })
						errList = nil
						break
					}
				} else {
					recLis = append(recLis, &PriceInfo{time.Now().Unix(), price})
					if statsNum > 0 {
						maxVal, minVal, avgVal, medVal := getStatsPrice(recLis, int64(statsNum))
						log(fmt.Sprintf("当前价格: %.2f|max:%.2f|min:%.2f|avg:%.2f|med:%.2f", price, maxVal, minVal, avgVal, medVal))
					} else {
						log(fmt.Sprintf("当前价格: %.2f", price))
					}

					profit := 10000/price*(price-buyPrice) - 50
					fyne.Do(func() {
						currEntry.SetText(fmt.Sprintf("%.2f", price))
						profitEntry.SetText(fmt.Sprintf("%.2f", profit))
					})

					errList = append(errList, 0)
					if len(errList) > 5 {
						errList = errList[len(errList)-5:]
					}

					// 仅 interval == 15 时记录
					go logPriceToDB(price) // 异步写入

					// 买入提醒
					if price <= targetBuyPrice {
						msg := fmt.Sprintf("\n买入平均价格: %.2f\n现价: %.2f\n目标买入价格: %.2f\n可以买入！", buyPrice, price, targetBuyPrice)
						log(msg)
						if notify && key != "" {
							go scSend(key, "买入提醒", msg)
						}
						showAlertPopup(msg)
						isRunning = false
						fyne.Do(func() { runButton.SetText("运行") })
						break
					}

					// 卖出提醒
					if price >= targetSellPrice {
						msg := fmt.Sprintf("\n买入平均价格: %.2f\n现价: %.2f\n目标卖出价格: %.2f\n可以卖出！", buyPrice, price, targetSellPrice)
						log(msg)
						if notify && key != "" {
							go scSend(key, "卖出提醒", msg)
						}
						showAlertPopup(msg)
						isRunning = false
						fyne.Do(func() { runButton.SetText("运行") })
						break
					}
				}
				for i := 0; i < interval; i++ {
					if isRunning {
						time.Sleep(time.Second)
					}
				}
			}
		}
	}()

	// 运行按钮
	runButton.OnTapped = func() {
		buttonMutex.Lock()
		defer buttonMutex.Unlock()
		if isRunning {
			isRunning = false
			runButton.SetText("运行")
			log("已暂停")
		} else {
			if buyPriceEntry.Text == "" || targetBuyPriceEntry.Text == "" ||
				targetSellPriceEntry.Text == "" || intervalEntry.Text == "" {
				log("请填写所有字段")
				return
			}
			isRunning = true
			stopChan = make(chan struct{})
			runButton.SetText("暂停")
			log(fmt.Sprintf("已启动，启用通知:%v", notify))
		}
	}

	// 布局（无表格）
	form := container.New(layout.NewFormLayout(),
		widget.NewLabel("买入平均价格："), buyPriceEntry,
		widget.NewLabel("目标买入价格："), targetBuyPriceEntry,
		widget.NewLabel("目标卖出价格："), targetSellPriceEntry,
		widget.NewLabel("当前买卖价格："), currEntry,
		widget.NewLabel("当前万元收益："), profitEntry,
		widget.NewLabel("间隔时间（秒）："), intervalEntry,
		widget.NewLabel("统计时间（分）："), statsEntry,
		widget.NewLabel("通知设置："), notifyCheck,
	)
	// 使用Border布局，让logScroll能够自动扩展
	topContent := container.NewVBox(
		form,
		runButton,
		widget.NewLabel("日志："),
	)

	content := container.NewBorder(
		topContent, // 顶部内容
		nil,        // 底部内容
		nil,        // 左侧内容
		nil,        // 右侧内容
		logScroll,  // 中心内容（自动扩展）
	)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

func sum(arr []int) int {
	total := 0
	for _, v := range arr {
		total += v
	}
	return total
}
