package main

import (
    "fmt"
    "net/http"
    "time"
    "strconv"
    "bytes"
    "os"
    "github.com/streadway/amqp"
    "strings"
    "encoding/json"
)

var (
  connection * amqp.Connection
  channel * amqp.Channel
  exchange = "StreamMetadata"
  routingKey = "StreamMetadata"
  queueName = "StreamMetadata"
)

const IcyValueWrapper = "'"

func (station * Station) publish(metadata string) error {
  metadataElements := strings.Split(metadata, ";")
  metadataInfo := map[string]string{}
  for _, element := range metadataElements {
    parts := strings.Split(element, "=")
    if len(parts) == 2 {
      var key, value string
      key = strings.Trim(parts[0], IcyValueWrapper)
      value = strings.Trim(parts[1], IcyValueWrapper)
      metadataInfo[key] = value
    }
  }
  metadataInfo["station_title"] = station.Title
  var err error
  var body []byte
  if body, err = json.Marshal(metadataInfo) ; err != nil {
    fmt.Printf("Error serializing json\n")
    panic(err)
  }

  if err := channel.Publish(
    exchange,   // publish to an exchange
    routingKey, // routing to 0 or more queues
    false,      // mandatory
    false,      // immediate
    amqp.Publishing{
      Headers:         amqp.Table{},
      ContentType:     "application/json",
      ContentEncoding: "",
      Body:            body,
      DeliveryMode:    amqp.Transient, // 1=non-persistent, 2=persistent
      Priority:        0,              // 0-9
      // a bunch of application/implementation-specific fields
    },
  ); err != nil {
    return fmt.Errorf("Exchange Publish: %s", err)
  }
  return nil
}

type Station struct {
  Title string
  StreamUrl string
}

type StreamParser struct {
  Station Station
  MetadataOffsetMark int
  skippedCount int
  currentMetaBuffer *bytes.Buffer
  metadataLength int
}
const ResetValue = -1
func newStreamParser() (*StreamParser) {
  parser := new(StreamParser)
  parser.metadataLength = ResetValue
  return parser;
}

func (parser * StreamParser) resetMetadata() {
  parser.currentMetaBuffer = nil
  parser.metadataLength = ResetValue
}

func (parser * StreamParser) hasReachedMetadataMark() (bool) {
  return parser.skippedCount == parser.MetadataOffsetMark;
}

func (parser * StreamParser) isExpectingMetadataLength() (bool) {
  return  parser.currentMetaBuffer != nil && parser.metadataLength == ResetValue;
}

func (parser * StreamParser) isReadingMetadata() (bool) {
  return parser.currentMetaBuffer != nil;
}

func (parser *StreamParser) Parse(buffer []byte) (err error) {
  // fmt.Printf("Parse Buffer\n")
  for _, b := range buffer {
    if parser.hasReachedMetadataMark() {
      // metadata mark
      parser.skippedCount = 0
      parser.resetMetadata()
      parser.currentMetaBuffer = &bytes.Buffer{}
    }
    if parser.isExpectingMetadataLength() {
      // metadata length byte
      parser.metadataLength = int(16 * b)
      if parser.metadataLength == 0 { // the song hasn't changed
        parser.resetMetadata()
      }
    } else if parser.isReadingMetadata() {
      parser.currentMetaBuffer.WriteByte(b)
      if parser.currentMetaBuffer.Len() == parser.metadataLength {
        // metatada was found
        metadataInfo := parser.currentMetaBuffer.String()
        fmt.Printf("%s:\n\t%s\n", parser.Station.Title, metadataInfo)
        parser.Station.publish(metadataInfo)
        parser.skippedCount = 0
        parser.resetMetadata()
      }
    } else {
      // this is audio, keep counting bytes until we reach the metadata mark
      parser.skippedCount++
    }
  }
  return nil
}

func listenRadio(station Station, quit chan Station) {
    client := &http.Client{}
    const MetaHeader = "Icy-Metaint"
    fmt.Printf("Listening %s (%s)\n", station.Title, station.StreamUrl)
    req, err := http.NewRequest("GET", station.StreamUrl, nil)
    if err != nil {
      fmt.Printf("HTTP Error %s", err)
      panic(err)
    }
    req.Header.Add("icy-metadata", "1")
    resp, err := client.Do(req)
    if err != nil {
      fmt.Printf("HTTP GET Error %s", err)
      panic(err)
    }
    offset, err := strconv.Atoi(resp.Header.Get(MetaHeader))
    parser := newStreamParser()
    parser.Station = station
    parser.MetadataOffsetMark = offset
    if err != nil {
      fmt.Printf("Corrupted icy-metaint", err)
      panic(err)
    }
    /*
    for key, v := range resp.Header {
      fmt.Printf("H %s, V %s\n", key, v)
    }*/
    for {
      buffer := make([]byte, 102400)
      read, err := resp.Body.Read(buffer)
      if err != nil {
        fmt.Printf("Read buffer error %s %s", station.Title, err)
      }
      //fmt.Printf("Read %d %d\n", read, len(buffer))
      err = parser.Parse(buffer[0:read])
      if err != nil {
        panic(err)
      }
      time.Sleep(1 * time.Second)
    }
    defer resp.Body.Close()
    quit <- station
}

func listenStations(stations []Station, quit chan Station) {
  for _, station := range stations {
    go listenRadio(station, quit)
  }
}

func main() {
  amqpURI := "amqp://guest:guest@localhost:5672"
  fmt.Printf("dialing %q", amqpURI)
  var err error;
  connection, err = amqp.Dial(amqpURI)
  if err != nil {
    fmt.Errorf("Queue connection error: %s", err)
    panic(err)
  }
  defer connection.Close()

  channel, err = connection.Channel()
  if err != nil {
    fmt.Errorf("Channel: %s", err)
    panic(err)
  }

  if err := channel.ExchangeDeclare(
    exchange,     // name
    "direct", // type
    true,         // durable
    false,        // auto-deleted
    false,        // internal
    true,        // noWait
    nil,          // arguments
  ); err != nil {
    fmt.Errorf("Exchange Declare: %s", err)
    panic(err)
  }
  if _, err := channel.QueueDeclare(
    queueName,
    true, // durable
    false, // autoDelete
    false, // exclusive
    true, // no wait
    amqp.Table{},
  ); err != nil {
    fmt.Errorf("Queue Declare: %s", err)
    panic(err)
  }
  if err := channel.QueueBind(
    queueName, // queueName
    routingKey, // key
    exchange, // exchange
    false, // no wait
    amqp.Table{},
  ); err != nil {
    fmt.Errorf("Queue Bind: %s", err)
    panic(err)
  }

  quit := make(chan Station)
  var stations = []Station{{Title: "Vocal Trance", StreamUrl: "http://pub4.di.fm:80/di_vocaltrance"}, {Title: "Techno", StreamUrl: "http://pub6.di.fm:80/di_techhouse_aac"}, {Title: "House", StreamUrl: "http://pub4.di.fm:80/di_house_aac"}, {Title: "Hardstyle", StreamUrl: "http://pub7.di.fm:80/di_hardstyle_aac"}}
  listenStations(stations, quit)
  fmt.Printf("Starting Worker %d\n", os.Getpid())
  quitCount := 0
  expectedQuits := len(stations)
  for {
    station := <-quit
    quitCount++
    fmt.Printf("Finished Station %s\n", station.Title)
    if quitCount == expectedQuits {
      break
    }
  }
  fmt.Printf("Finishing Worker\n")
}

