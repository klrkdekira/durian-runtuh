package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
)

type (
	Tender struct {
		Title    string `json:"title"`
		Category string `json:"category"`
		Ministry string `json:"ministry"`
		Company  string `json:"company"`
		Value    int64  `json:"value"`
		Reason   string `json:"reason"`
	}
)

const target = "http://myprocurement.treasury.gov.my/templates/theme427/rttender.php"

// hardcoded page number
const pageNum = 15

func main() {
	// bootstrapping
	runtime.GOMAXPROCS(runtime.NumCPU())
	os.MkdirAll("downloads", 0755)

	downloadState := make(chan struct{}, 1)
	processState := make(chan struct{}, 1)

	jobChans := make([]chan error, pageNum)
	docsChan := make(chan int) // variable length, execution state not guaranteed

	var downloaderWg sync.WaitGroup
	downloaderWg.Add(15)

	for i := 0; i < pageNum; i++ {
		workerChan := make(chan error, 1)
		go downloadHTML(i+1, workerChan, docsChan)
		jobChans[i] = workerChan
	}

	go func() {
		for _, errChan := range jobChans {
			if err := <-errChan; err != nil {
				fmt.Println(err)
			}
			downloaderWg.Done()
		}

		downloaderWg.Wait()
		downloadState <- struct{}{}
	}()

	go processDocument(docsChan, processState)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		for {
			select {
			case <-downloadState:
				fmt.Println("just downloaded everything, still processing...")
				wg.Done()
				close(docsChan)
			case <-processState:
				fmt.Println("done with processing!")
				wg.Done()
			}
		}
	}()
	wg.Wait()

	fmt.Println("done!")
}

func processDocument(doc chan int, processState chan struct{}) {
	tenders := make([]*Tender, 0)
	for {
		d, more := <-doc
		if more {
			info, err := extractData(d)
			if err != nil {
				fmt.Println(err)
			} else {
				tenders = append(tenders, info...)
			}
		} else {
			break
		}
	}
	dump, err := json.Marshal(tenders)
	if err != nil {
		fmt.Println(err)
	}

	if err := ioutil.WriteFile("results.json", dump, 0644); err != nil {
		fmt.Println(err)
	}
	processState <- struct{}{}
}

func extractData(docNum int) ([]*Tender, error) {
	filepath := fmt.Sprintf("downloads/%d.html", docNum)
	reader, err := os.Open(filepath)
	if err != nil {
		fmt.Printf("error opening file, %v", err)
		return nil, err
	}

	soup, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		fmt.Printf("error parsing file, %v", err)
		return nil, err
	}

	tenders := make([]*Tender, 0)
	soup.Find("table tbody tr:not(:first-child)").Each(func(i int, cols *goquery.Selection) {
		tender := &Tender{}
		cols.Find("td:not(:first-child)").Each(func(i int, content *goquery.Selection) {
			switch i {
			case 0:
				tender.Title = content.Text()
			case 1:
				tender.Category = content.Text()
			case 2:
				tender.Ministry = content.Text()
			case 3:
				tender.Company = content.Text()
			case 4:
				stringVal := strings.Replace(content.Text(), ",", "", -1)
				// forgive my cheap hack
				val, err := strconv.ParseInt(strings.Split(stringVal, ".")[0], 10, 64)
				if err != nil {
					log.Println(err)
				}
				tender.Value = val
			case 5:
				tender.Reason = content.Text()
			}
		})
		tenders = append(tenders, tender)
	})
	return tenders, nil
}

func downloadHTML(pageNum int, workerChan chan error, docsChan chan int) {
	client := &http.Client{}
	params := url.Values{}
	params.Set("page", fmt.Sprintf("%d", pageNum))
	mark := fmt.Sprintf("%s?%s", target, params.Encode())
	req, err := http.NewRequest("GET", mark, nil)
	if err != nil {
		// no retry, thanks
		workerChan <- err
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		workerChan <- err
		return
	}
	defer resp.Body.Close()

	// has to read them first
	// consume more memory, should copy to file directly
	// but good for parsing work, TODO
	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		workerChan <- err
		return
	}

	filename := fmt.Sprintf("downloads/%d.html", pageNum)
	// this is so going to block
	if err := ioutil.WriteFile(filename, content, 0644); err != nil {
		workerChan <- err
		return
	}

	// finished safely
	workerChan <- nil
	docsChan <- int(pageNum)
}
