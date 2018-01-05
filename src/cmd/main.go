package main

import (
	"net/http"
	"fmt"
	"io/ioutil"
	"encoding/json"
	"strconv"
	"strings"
	"gopkg.in/mgo.v2"
	"os"
	"gopkg.in/mgo.v2/bson"
	"sort"
	"bytes"
)

var accessToken, refreshToken, dbURL, albumURL, dbName, cName, clientID, clientSecret string

type ImageStruct struct {
	URL string `json:"url"`
	Tags []TagStruct `json:"tags"`
}

type TagStruct struct {
	Tag string `json:"tag"`
}

type RefreshStruct struct {
	RefreshToken string `json:"refresh_token"`
	ClientID string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	GrantType string `json:"grant_type"`
}

func RefreshAccessToken() {
	client := &http.Client{}
	request := RefreshStruct{
		refreshToken,
		clientID,
		clientSecret,
		"refresh_token"}
	requestJson, err := json.Marshal(request)
	imgurReq, err := http.NewRequest(http.MethodPost, "https://api.imgur.com/oauth2/token", bytes.NewBuffer(requestJson))
	if err != nil {
		return
	}
	imgurReq.Header.Set("Content-Type", "application/json")
	response, err := client.Do(imgurReq)
	if err != nil {
		return
	}
	defer response.Body.Close()

	responseBody, err := ioutil.ReadAll(response.Body)
	responseJson := make(map[string]interface{})
	err = json.Unmarshal(responseBody, &responseJson)
	if err != nil {
		return
	}
	os.Setenv("ACCESS_TOKEN", responseJson["access_token"].(string))
	accessToken = os.Getenv("ACCESS_TOKEN")
}

func CleanupHandler(w http.ResponseWriter, r *http.Request) {
	session, err := mgo.Dial(dbURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer session.Close()
	var documents []interface{}
	err = session.DB(dbName).C(cName).Find(nil).All(&documents)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	sort.SliceStable(documents, func(i, j int) bool {
		return documents[i].(bson.M)["url"].(string) < documents[j].(bson.M)["url"].(string) })
	for i, prevURL := 0, ""; i < len(documents); i++ {
		if documents[i].(bson.M)["url"] == prevURL {
			err = session.DB(dbName).C(cName).RemoveId(documents[i].(bson.M)["_id"].(bson.ObjectId))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		prevURL = documents[i].(bson.M)["url"].(string)
	}
	fmt.Fprintln(w, "Cleanup completed.")
}

func InsertHandler(w http.ResponseWriter, r *http.Request){
	switch r.Method {
	case "GET":
		client := &http.Client{}
		session, err := mgo.Dial(dbURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusFailedDependency)
		}
		imgurReq, err := http.NewRequest(http.MethodGet, albumURL, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusFailedDependency)
			return
		}
		authorizationToken := "Bearer " + accessToken
		imgurReq.Header.Set("Authorization", authorizationToken)
		imgurResp, err := client.Do(imgurReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if imgurResp.StatusCode == http.StatusForbidden {
			RefreshAccessToken()
			authorizationToken = "Bearer " + accessToken
			imgurReq.Header.Set("Authorization", authorizationToken)
			imgurResp, err = client.Do(imgurReq)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		body, err := ioutil.ReadAll(imgurResp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		imgurJson := make(map[string]interface{})
		err = json.Unmarshal(body, &imgurJson)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		imgurData := imgurJson["data"].(map[string]interface{})
		imgurImages := imgurData["images"].([]interface{})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		output := `<style>
	img {
		width: 150px;
	}
	</style>
	<form action="" method="post">`
		for i, j := 0, 0; i < len(imgurImages); i++ {
			currentImg := imgurImages[i].(map[string]interface{})["link"].(string)
			count, err := session.DB(dbName).C(cName).Find(bson.M{"url": currentImg}).Count()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if count == 0 {
				output += `<img src="` + currentImg + `"><br>
				<a href="` + currentImg + `">` + currentImg + `</a><br>
				<input type="hidden" name="url[` + strconv.Itoa(j) + `]" value="` + currentImg + `">
				<input type="text" name="tags[` + strconv.Itoa(j) + `]"><br>`
				j++
			}
		}
		output += `<input type="submit" value="Submit">
	</form>`
		fmt.Fprintln(w, output)
	case "POST":
		session, err := mgo.Dial(dbURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		defer session.Close()

		r.ParseForm()
		for i := 0; i < len(r.Form)/2; i++{
			currentURL := r.Form["url[" + strconv.Itoa(i) + "]"]
			currentTags := r.Form["tags[" + strconv.Itoa(i) + "]"]
			tagArray := strings.Split(currentTags[0], " ")
			currentJson := ImageStruct{}
			currentJson.URL = currentURL[0]
			for j := 0; j < len(tagArray); j++ {
				if tagArray[j] != "" {
					currentJson.Tags = append(currentJson.Tags, TagStruct{tagArray[j]})
				}
			}
			if len(currentJson.Tags) >= 2 {
				err = session.DB(dbName).C(cName).Insert(currentJson)
				if err != nil {
					http.Error(w, err.Error(), http.StatusFailedDependency)
					return
				}
			}
		}

	default:
		http.Error(w, "Unexpected method", http.StatusBadRequest)
	}

}

func SearchHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		searchForm := `<form action="" method="post">
		<input type="text" name="search"><br>
		<input type="submit" value="Submit">
	</form>`
	fmt.Fprintln(w, searchForm)
	case "POST":
		session, err := mgo.Dial(dbURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer session.Close()

		r.ParseForm()
		formString := r.Form["search"]
		searchTags := strings.Split(formString[0], " ")
		var searchQuery []bson.M
		for i := 0; i < len(searchTags); i++ {
			searchQuery = append(searchQuery, bson.M{"tag": searchTags[i]})
		}
		var searchResults []interface{}
		err = session.DB(dbName).C(cName).Find(bson.M{"tags" : bson.M{"$all" : searchQuery}}).All(&searchResults)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(searchResults) > 0 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			output := `<style>
	img {
		width: 150px;
	}
	</style>`
			for i := 0; i < len(searchResults); i++ {
				output += `<img src="` + searchResults[i].(bson.M)["url"].(string) + `"><br>
				<a href="` + searchResults[i].(bson.M)["url"].(string) + `">` +
					searchResults[i].(bson.M)["url"].(string) + `</a><br>`
			}
			fmt.Fprintln(w, output)
		} else {
			fmt.Fprintln(w, "No matching images found.")
		}
	default:
		http.Error(w, "Unexpected method", http.StatusBadRequest)
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	accessToken = os.Getenv("ACCESS_TOKEN")
	refreshToken = os.Getenv("REFRESH_TOKEN")
	dbURL = os.Getenv("DB_URL")
	albumURL = os.Getenv("ALBUM_URL")
	dbName = os.Getenv("DB_NAME")
	cName = os.Getenv("C_NAME")
	clientID = os.Getenv("CLIENT_ID")
	clientSecret = os.Getenv("CLIENT_SECRET")

	http.HandleFunc("/", SearchHandler)
	http.HandleFunc("/addimages/", InsertHandler)
	http.HandleFunc("/cleanup/", CleanupHandler)
	http.ListenAndServe(":"+port, nil)
}
