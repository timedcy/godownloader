package downloader

import (
	"crawler/downloader/graphite"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"
)

type RedirectorHandler struct {
	metricSender   *graphite.Client
	processedLinks *BloomFilter
	linksChannel   []chan string
	urlFilter      *URLFilter
	dnsCache       map[string]string
	usedChannels   map[int]int64
}

func (self *RedirectorHandler) Match(link string) int {
	return self.urlFilter.Match(link)
}

func (self *RedirectorHandler) GetIP(host string) string {
	ip, ok := self.dnsCache[host]
	if ok {
		return ip
	}
	ip = LoopUpIp(host)
	if ip == "" {
		ip = host
	}
	log.Println("dns lookup", host, ip)
	return ip
}

func (self *RedirectorHandler) Redirect(ci int) {
	priority := ci/ConfigInstance().RedirectChanNum + 1
	log.Println("priority of chan ", ci, "is", priority)
	n := 0
	for link := range self.linksChannel[ci] {
		n += 1
		log.Println("redirect : ", link)
		SetBloomFilter(link)
		pb := PostBody{}
		pb.Links = []string{link}
		jsonBlob, err := json.Marshal(&pb)
		if err == nil {
			req := make(map[string]string)
			req["links"] = string(jsonBlob)
			PostHTTPRequest(ConfigInstance().DownloaderHost, req)
		}
		time.Sleep(60 * time.Second / time.Duration(ConfigInstance().PagePerMinute) / time.Duration(priority))
		if n%100 == 0 {
			time.Sleep(time.Duration(rand.Int63n(600)) * time.Second)
			log.Println("channel sleep : ", ci)
		}
	}
}

func NewRedirectorHandler() *RedirectorHandler {
	ret := RedirectorHandler{}
	ret.metricSender, _ = graphite.New(ConfigInstance().GraphiteHost, "")
	ret.linksChannel = []chan string{}
	for i := 0; i < ConfigInstance().RedirectChanNum*2; i++ {
		ret.linksChannel = append(ret.linksChannel, make(chan string, ConfigInstance().RedirectChanSize))
	}
	ret.processedLinks = NewBloomFilter()
	ret.usedChannels = make(map[int]int64)
	ret.urlFilter = NewURLFilter()
	for i := 0; i < ConfigInstance().RedirectChanNum*2; i++ {
		go ret.Redirect(i)
	}
	return &ret
}

func (self *RedirectorHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Println(r)
		}
	}()

	links := req.PostFormValue("links")
	if len(links) > 0 {
		pb := PostBody{}
		json.Unmarshal([]byte(links), &pb)

		for _, link := range pb.Links {

			priority := self.Match(link)
			if priority <= 0 {
				continue
			}
			if rand.Float64() > 0.1 {
				if self.processedLinks.Contains(link) {
					continue
				}
			}
			if rand.Float64() < 0.5 && priority == 1 {
				continue
			}
			ci := Hash(ExtractMainDomain(link))%int32(ConfigInstance().RedirectChanNum) + int32((priority-1)*ConfigInstance().RedirectChanNum)
			if len(self.linksChannel[ci]) < ConfigInstance().RedirectChanSize {
				if CheckBloomFilter(link) {
					log.Println("downloaded before : ", link)
					continue
				}

				log.Println("channel ", ci, " recv link : ", link, ExtractMainDomain(link))
				self.processedLinks.Add(link)
				self.linksChannel[ci] <- link
				self.usedChannels[int(ci)] = time.Now().Unix()
			}
		}
	}

	if rand.Float64() < 0.2 {
		linkChannelTotalSize := 0
		maxChannelSize := 0
		nonEmptyQueueCount := 0

		for _, cn := range self.linksChannel {

			size := len(cn)

			linkChannelTotalSize += size

			if maxChannelSize < size {
				maxChannelSize = size
			}
			if size > 0 {
				nonEmptyQueueCount += 1
			}
		}

		usedChannelCount := 0
		currentTm := time.Now().Unix()
		for _, tm := range self.usedChannels {
			if currentTm-tm > 120 {
				continue
			}
			usedChannelCount += 1
		}
		self.metricSender.Gauge("crawler.redirector."+GetHostName()+"."+Port+".channelsize", int64(linkChannelTotalSize), 1.0)
		self.metricSender.Gauge("crawler.redirector."+GetHostName()+"."+Port+".maxchannelsize", int64(maxChannelSize), 1.0)
		self.metricSender.Gauge("crawler.redirector."+GetHostName()+"."+Port+".nonemptychannelcount", int64(nonEmptyQueueCount), 1.0)
		self.metricSender.Gauge("crawler.redirector."+GetHostName()+"."+Port+".usedchannelcount", int64(usedChannelCount), 1.0)
	}
	fmt.Fprint(w, "")
}
