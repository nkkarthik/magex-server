package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"time"
	"bufio"
	"os"
	"strings"

	"github.com/satori/go.uuid"
)

type Props struct {
	dispenseSuccess bool
}

func ReadProps() (p Props) {
	propsJson, err := ioutil.ReadFile("settings.json")
	if err != nil {
		log.Println("Could not read settings.json")
	}
	log.Printf("settings: %v", propsJson)
	json.Unmarshal(propsJson, &p)
	log.Printf("settings: %v", p)
	return
}

func sendAsyncEvents(conn net.Conn, writeCh chan Message) {
	log.Println("Starting async event sender")
	for {
		in := bufio.NewReader(os.Stdin)
		code, _ := in.ReadString('\n')
		code = strings.TrimSpace(code)
		if code == "do" {
			ae := AsyncEvent(103, "door open")
			writeCh <- ae
		}
		if code == "dc" {
			ae := AsyncEvent(104, "door closed")
			writeCh <- ae
		}
		// time.Sleep(30 * time.Second)
	}
}

func getDispenseSuccessCode() (code int) {
	success := ReadProps().dispenseSuccess
	if !success {
		code = 0
	}
	return
}

func sendDispenseComplete(request map[string]map[string]interface{}, writeCh chan Message) {
	id := request["dispenseRequest"]["id"]

	log.Printf("Dispense requested. id: %s", id)
	time.Sleep(2 * time.Second)

	code := getDispenseSuccessCode()
	code = 0
	log.Printf("Sending dispense success code %d. id: %s", code, id)

	writeCh <- createMessage("dispenseComplete", map[string]interface{}{
		"code":        code,
		"description": "Success",
		"id":          id,
	})
}

func handleCommands(conn net.Conn, readCh chan Message, writeCh chan Message) {
	for c := range readCh {
		log.Printf("Got command %v %v %v %v", c.msgType, c.msgId, c.length, c.msg)
		writeCh <- c.Ack()

		if c.length < 2 {
			continue
		}

		var command map[string]map[string]interface{}
		err := json.Unmarshal([]byte(c.msg), &command)
		if err != nil {
			log.Printf("Error unmarshalling %s", err)
		}

		if _, ok := command["dispenseRequest"]; ok {
			go sendDispenseComplete(command, writeCh)
			continue
		}

		for k, _ := range command {
			writeCh <- messageFromFile(k + ".json")
			break
		}
	}
}

func readMessages(conn net.Conn, commands chan Message, ack chan Message) {
	defer close(commands)
	defer close(ack)
	for {
		m, err := ReadMessage(conn)
		if err != nil {
			log.Printf("Disconnected: %s", err)
			return
		}
		if m.msgType == 0 {
			commands <- m
		}
		if m.msgType == 1 {
			ack <- m
		}
	}
}

func writeMessages(conn net.Conn, messages chan Message, ack chan Message) {
	for msg := range messages {
		_, err := conn.Write(msg.Bytes())
		if err != nil {
			log.Printf("Failed to write: %s", err)
		}

		if msg.msgType == 1 {
			// don't expect ack for an ack
			continue
		}

		log.Println("Waiting for ack response")
		a, ok := <-ack
		if !ok {
			return
		}
		if a.msgType != 1 {
			log.Fatalf("Invalid msgtype %v", a)
		}
		if bytes.Compare(msg.msgId, a.msgId) != 0 {
			log.Fatalf("UUID did not match sent %s got %s", msg.msgId, a.msgId)
		}
		log.Println("Got ack response")
	}
}

func serve() {
	listener, err := net.Listen("tcp", ":16022")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer listener.Close()
	log.Println("Waiting for connection: 16022")

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Println(err)
			return
		}
		readCh := make(chan Message)
		writeCh := make(chan Message)
		ackCh := make(chan Message)

		go readMessages(conn, readCh, ackCh)
		go writeMessages(conn, writeCh, ackCh)

		go sendAsyncEvents(conn, writeCh)
		go handleCommands(conn, readCh, writeCh)
	}
}

type Message struct {
	msgType byte
	msgId   []byte
	length  uint32
	msg     string
}

func get_uuid() []byte {
	u := uuid.NewV4()
	//out, err := exec.Command("uuidgen").Output()
	//if err != nil {
	//log.Fatal(err)
	//}
	return u[:16]
}

func ReadMessage(in io.Reader) (m Message, err error) {
	syn := byte(0x16)
	for i := 0; i < 2; i++ {
		var b byte
		err = binary.Read(in, binary.BigEndian, &b)
		if err != nil {
			return
		}
		if b != syn {
			err = fmt.Errorf("syn expected: got %v", b)
		}
		//log.Println("Got syn")
	}
	err = binary.Read(in, binary.BigEndian, &m.msgType)
	if m.msgType != 0 && m.msgType != 1 {
		err = fmt.Errorf("invalid msgType %v", m.msgType)
		return
	}
	log.Printf("Got msgType %d", m.msgType)

	var uuid [16]byte
	err = binary.Read(in, binary.BigEndian, &uuid)
	//err = binary.Read(in, binary.LittleEndian, &uuid)
	if err != nil {
		log.Fatalf("cant read uuid %s", err)
		return
	}
	m.msgId = uuid[:]
	log.Printf("Got uuid %s", m.msgId)

	err = binary.Read(in, binary.BigEndian, &m.length)
	if err != nil {
		log.Fatalf("cant read len %s", err)
		return
	}
	log.Printf("Got length %d", m.length)

	var payload bytes.Buffer
	for i := 0; i < int(m.length); i++ {
		var b byte
		err = binary.Read(in, binary.BigEndian, &b)
		if err != nil {
			log.Fatalf("cant read payload %s", err)
			return
		}
		payload.WriteByte(b)
	}
	m.msg = string(payload.Bytes())
	return
}

func (m *Message) Bytes() []byte {
	syn := byte(0x16)
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, syn)
	binary.Write(&buf, binary.BigEndian, syn)
	binary.Write(&buf, binary.BigEndian, m.msgType)
	binary.Write(&buf, binary.BigEndian, m.msgId)
	//binary.Write(&buf, binary.LittleEndian, m.msgId)
	binary.Write(&buf, binary.BigEndian, m.length)
	binary.Write(&buf, binary.BigEndian, []byte(m.msg))
	return buf.Bytes()
}

func jsonBytes(t string, data map[string]interface{}) []byte {
	m := map[string]interface{}{t: data}
	ms, err := json.Marshal(m)
	if err != nil {
		log.Fatal(err)
	}
	return ms
}

func createMessage(t string, data map[string]interface{}) Message {
	ms := jsonBytes(t, data)
	log.Printf("%s", ms)
	return Message{0, get_uuid(), uint32(len(ms)), string(ms)}
}

func AsyncEvent(code int, s string) Message {
	return createMessage("asyncEvent", map[string]interface{}{
		"code":        code,
		"description": s,
		"level":       "INFO",
		"timestamp":   "2017-05-04 10:53:24.055",
	})
}

func messageFromFile(file string) Message {
	d, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatalf("Error: could not read %s", file)
	}
	ds := string(d)
	return Message{0, get_uuid(), uint32(len(ds)), ds}
}

func (m *Message) Ack() (a Message) {
	a.msgType = 1
	a.msgId = m.msgId
	if m.length == 1 {
		a.length = 1
		a.msg = m.msg
	}
	return
}

func main() {
	serve()
}
