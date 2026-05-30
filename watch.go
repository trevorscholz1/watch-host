package main

import (
	"context"
	"encoding/gob"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

var (
	youtubeService *youtube.Service
	templates      *template.Template
	store          *sessions.CookieStore
)

func init() {
	gob.Register([]SearchResult{})
}

type SearchResult struct {
	ChannelTitle      string
	ChannelID         string
	UploadsPlaylistID string
	VideoCount        uint64
}

type VideoData struct {
	Title      string
	VideoID    string
	Error      string
	ChannelID  string
	ChannelLen int
	Interval   string
	UploadDate string
}

func main() {
	var err error

	if os.Getenv("RENDER") == "" {
		err := godotenv.Load()
		if err != nil {
			log.Println("Warning: no .env file found, skipping")
		}
	}
	api_key := os.Getenv("API_KEY")

	session_key := os.Getenv("SESSION_KEY")
	if session_key == "" {
		log.Fatal("SESSION_KEY not set")
	}
	store = sessions.NewCookieStore([]byte(session_key))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 30,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}

	ctx := context.Background()
	youtubeService, err = youtube.NewService(ctx, option.WithAPIKey(api_key))
	if err != nil {
		log.Fatal(err)
	}

	templates, err = template.ParseFiles("index.html", "results.html")
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/", handleHome)
	http.HandleFunc("/search", handleSearch)
	http.HandleFunc("/add-channel", handleAddChannel)
	http.HandleFunc("/remove-channel", handleRemoveChannel)
	http.HandleFunc("/random", handleRandomVideo)

	log.Println("Server starting on http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func getChannels(r *http.Request) []SearchResult {
	session, err := store.Get(r, "SESSION")
	if err != nil {
		log.Println("Session get error:", err)
		return []SearchResult{}
	}

	val, ok := session.Values["channels"].([]SearchResult)
	if !ok {
		return []SearchResult{}
	}

	return val
}

func saveChannels(w http.ResponseWriter, r *http.Request, channels []SearchResult) error {
	session, err := store.Get(r, "SESSION")
	if err != nil {
		return err
	}

	session.Values["channels"] = channels

	if err := session.Save(r, w); err != nil {
		return err
	}

	return nil
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	channels := getChannels(r)
	templates.ExecuteTemplate(w, "index.html", map[string]interface{}{
		"Channels": channels,
	})
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	channels := getChannels(r)
	query := r.FormValue("query")
	if query == "" {
		templates.ExecuteTemplate(w, "index.html", map[string]interface{}{
			"Error":    "Please enter a search query",
			"Channels": channels,
		})
		return
	}

	results, err := searchChannels(youtubeService, query)
	if err != nil {
		templates.ExecuteTemplate(w, "index.html", map[string]interface{}{
			"Error":    "Error searching for channels: " + err.Error(),
			"Channels": channels,
		})
		return
	}

	templates.ExecuteTemplate(w, "results.html", map[string]interface{}{
		"Results":  results,
		"Channels": channels,
	})
}

func handleAddChannel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	channelTitle := r.Form.Get("channel_title")
	channelID := r.Form.Get("channel_id")
	uploadsPlaylistID := r.Form.Get("uploads_playlist_id")
	videoCountStr := r.Form.Get("video_count")
	videoCount, _ := strconv.ParseUint(videoCountStr, 10, 64)

	channels := getChannels(r)

	for _, ch := range channels {
		if ch.ChannelID == channelID {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}

	channels = append(channels, SearchResult{
		ChannelTitle:      channelTitle,
		ChannelID:         channelID,
		UploadsPlaylistID: uploadsPlaylistID,
		VideoCount:        videoCount,
	})

	if err := saveChannels(w, r, channels); err != nil {
		log.Println("Error saving:", err)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleRemoveChannel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	channelID := r.FormValue("channel_id")
	channels := getChannels(r)

	for i, ch := range channels {
		if ch.ChannelID == channelID {
			channels = append(channels[:i], channels[i+1:]...)
			break
		}
	}

	err := saveChannels(w, r, channels)
	if err != nil {
		log.Println("Error saving channels:", err)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleRandomVideo(w http.ResponseWriter, r *http.Request) {
	channels := getChannels(r)

	if len(channels) == 0 {
		templates.ExecuteTemplate(w, "index.html", map[string]interface{}{
			"Error":    "No channels selected",
			"Channels": channels,
		})
		return
	}

	channelIndex := rand.Intn(len(channels))
	selectedChannel := channels[channelIndex]
	log.Println("Selected Channel:", channelIndex)

	video, err := getRandomVideo(youtubeService, selectedChannel.ChannelID, selectedChannel.UploadsPlaylistID, selectedChannel.VideoCount)
	if err != nil {
		templates.ExecuteTemplate(w, "index.html", map[string]interface{}{
			"Error":    "Error fetching random video: " + err.Error(),
			"Channels": channels,
		})
		return
	}

	duration, err := getVideoDuration(youtubeService, video.Snippet.ResourceId.VideoId)
	if err != nil {
		templates.ExecuteTemplate(w, "index.html", map[string]interface{}{
			"Error":    "Error fetching video duration: " + err.Error(),
			"Channels": channels,
		})
		return
	}

	start, end := getRandomInterval(duration)
	data := VideoData{
		Title:      video.Snippet.Title,
		VideoID:    video.Snippet.ResourceId.VideoId,
		ChannelID:  video.Snippet.ChannelId,
		ChannelLen: int(selectedChannel.VideoCount),
		Interval:   fmt.Sprintf("%s - %s", start, end),
		UploadDate: video.Snippet.PublishedAt,
	}

	templates.ExecuteTemplate(w, "index.html", map[string]interface{}{
		"VideoData": data,
		"Channels":  channels,
	})
}

func getRandomVideo(service *youtube.Service, channelID string, uploadsPlaylistID string, videoCount uint64) (*youtube.PlaylistItem, error) {
	// TODO: Legacy data might not have cached metadata. Implement a permanent migration.
	if uploadsPlaylistID == "" || videoCount == 0 {
		log.Println("Metadata missing, performing fallback API call for channel:", channelID)
		channelsResponse, err := service.Channels.List([]string{"contentDetails", "statistics"}).Id(channelID).Do()
		if err != nil {
			return nil, err
		}
		if len(channelsResponse.Items) == 0 {
			return nil, fmt.Errorf("channel not found")
		}
		uploadsPlaylistID = channelsResponse.Items[0].ContentDetails.RelatedPlaylists.Uploads
		videoCount = channelsResponse.Items[0].Statistics.VideoCount
	}

	if videoCount == 0 {
		return nil, fmt.Errorf("no videos in channel")
	}

	randomIndex := rand.Int63n(int64(videoCount))
	log.Println("Selected Video", randomIndex, "from: ", videoCount)

	pageSize := int64(50)
	pageToFetch := randomIndex / pageSize
	itemOnPage := randomIndex % pageSize

	playlistItemsRequest := service.PlaylistItems.List([]string{"snippet", "contentDetails"}).
		PlaylistId(uploadsPlaylistID).
		MaxResults(pageSize)

	for i := int64(0); i < pageToFetch; i++ {
		response, err := playlistItemsRequest.Do()
		if err != nil {
			return nil, err
		}
		if response.NextPageToken == "" {
			return nil, fmt.Errorf("ran out of pages before reaching target video")
		}
		playlistItemsRequest = playlistItemsRequest.PageToken(response.NextPageToken)
	}

	playlistItemsResponse, err := playlistItemsRequest.Do()
	if err != nil {
		return nil, err
	}

	if len(playlistItemsResponse.Items) <= int(itemOnPage) {
		return nil, fmt.Errorf("video index out of range")
	}

	return playlistItemsResponse.Items[itemOnPage], nil
}

func searchChannels(service *youtube.Service, query string) ([]SearchResult, error) {
	call := service.Search.List([]string{"snippet"}).Q(query).Type("channel").MaxResults(10)
	response, err := call.Do()
	if err != nil {
		return nil, err
	}

	var channelIDs []string
	var results []SearchResult
	for _, item := range response.Items {
		channelIDs = append(channelIDs, item.Snippet.ChannelId)
		results = append(results, SearchResult{
			ChannelTitle: item.Snippet.ChannelTitle,
			ChannelID:    item.Snippet.ChannelId,
		})
	}

	if len(channelIDs) > 0 {
		channelsResponse, err := service.Channels.List([]string{"contentDetails", "statistics"}).Id(strings.Join(channelIDs, ",")).Do()
		if err != nil {
			return nil, err
		}

		channelMap := make(map[string]*youtube.Channel)
		for _, ch := range channelsResponse.Items {
			channelMap[ch.Id] = ch
		}

		for i := range results {
			if ch, ok := channelMap[results[i].ChannelID]; ok {
				results[i].UploadsPlaylistID = ch.ContentDetails.RelatedPlaylists.Uploads
				results[i].VideoCount = ch.Statistics.VideoCount
			}
		}
	}

	return results, nil
}

func searchShows() {
}

func getVideoDuration(service *youtube.Service, videoID string) (time.Duration, error) {
	call := service.Videos.List([]string{"contentDetails"}).Id(videoID)
	response, err := call.Do()
	if err != nil {
		return 0, err
	}
	if len(response.Items) == 0 {
		return 0, fmt.Errorf("video not found")
	}
	duration, err := parseDuration(response.Items[0].ContentDetails.Duration)
	if err != nil {
		return 0, err
	}
	return duration, nil
}

func parseDuration(duration string) (time.Duration, error) {
	re := regexp.MustCompile(`PT(\d+H)?(\d+M)?(\d+S)?`)
	matches := re.FindStringSubmatch(duration)
	var hours, minutes, seconds int64
	if matches[1] != "" {
		hours, _ = strconv.ParseInt(matches[1][:len(matches[1])-1], 10, 64)
	}
	if matches[2] != "" {
		minutes, _ = strconv.ParseInt(matches[2][:len(matches[2])-1], 10, 64)
	}
	if matches[3] != "" {
		seconds, _ = strconv.ParseInt(matches[3][:len(matches[3])-1], 10, 64)
	}
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second, nil
}

func getRandomInterval(duration time.Duration) (string, string) {
	if duration <= 30*time.Minute {
		return "", ""
	}
	maxStart := duration - 30*time.Minute
	start := time.Duration(rand.Int63n(int64(maxStart)))
	end := start + 30*time.Minute
	return fmt.Sprintf("%02d:%02d", int(start.Minutes()), int(start.Seconds())%60),
		fmt.Sprintf("%02d:%02d", int(end.Minutes()), int(end.Seconds())%60)
}
