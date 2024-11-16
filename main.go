package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type MessageType int

const (
	PUBLIC_MESSAGE MessageType = iota
	PRIVATE_MESSAGE
	SYSTEM_MESSAGE
	USER_LIST
	STATUS_UPDATE
)

type Message struct {
	Type      MessageType `json:"type"`
	Sender    string      `json:"sender"`
	Content   string      `json:"content"`
	Recipient string      `json:"recipient,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

type Client struct {
	conn     net.Conn
	nickname string
	status   string
	rooms    map[string]bool
	mutex    sync.RWMutex
}

type ChatRoom struct {
	name     string
	clients  map[*Client]bool
	messages []Message
}

type Server struct {
	clients    map[*Client]bool
	rooms      map[string]*ChatRoom
	mutex      sync.RWMutex
	commands   map[string]func(*Client, []string)
	history    []Message
	maxHistory int
}

func NewServer() *Server {
	s := &Server{
		clients:    make(map[*Client]bool),
		rooms:      make(map[string]*ChatRoom),
		commands:   make(map[string]func(*Client, []string)),
		maxHistory: 100,
	}
	s.setupCommands()
	s.createRoom("general")
	return s
}

func (s *Server) setupCommands() {
	s.commands = map[string]func(*Client, []string){
		"/nick": func(c *Client, args []string) {
			if len(args) < 2 {
				s.sendSystemMessage(c, "Usage: /nick <new_nickname>")
				return
			}
			oldNick := c.nickname
			c.nickname = args[1]
			s.broadcast(Message{
				Type:      SYSTEM_MESSAGE,
				Content:   fmt.Sprintf("%s is now known as %s", oldNick, c.nickname),
				Timestamp: time.Now(),
			}, nil)
		},
		"/msg": func(c *Client, args []string) {
			if len(args) < 3 {
				s.sendSystemMessage(c, "Usage: /msg <nickname> <message>")
				return
			}
			recipient := args[1]
			content := strings.Join(args[2:], " ")
			s.sendPrivateMessage(c, recipient, content)
		},
		"/status": func(c *Client, args []string) {
			if len(args) < 2 {
				s.sendSystemMessage(c, "Usage: /status <status_message>")
				return
			}
			c.status = strings.Join(args[1:], " ")
			s.broadcast(Message{
				Type:      STATUS_UPDATE,
				Sender:    c.nickname,
				Content:   c.status,
				Timestamp: time.Now(),
			}, nil)
		},
		"/join": func(c *Client, args []string) {
			if len(args) < 2 {
				s.sendSystemMessage(c, "Usage: /join <room_name>")
				return
			}
			roomName := args[1]
			s.joinRoom(c, roomName)
		},
		"/leave": func(c *Client, args []string) {
			if len(args) < 2 {
				s.sendSystemMessage(c, "Usage: /leave <room_name>")
				return
			}
			roomName := args[1]
			s.leaveRoom(c, roomName)
		},
		"/list": func(c *Client, args []string) {
			var users []string
			for client := range s.clients {
				status := client.status
				if status == "" {
					status = "online"
				}
				users = append(users, fmt.Sprintf("%s (%s)", client.nickname, status))
			}
			s.sendSystemMessage(c, fmt.Sprintf("Online users:\n%s", strings.Join(users, "\n")))
		},
		"/rooms": func(c *Client, args []string) {
			var rooms []string
			for name, room := range s.rooms {
				rooms = append(rooms, fmt.Sprintf("%s (%d users)", name, len(room.clients)))
			}
			s.sendSystemMessage(c, fmt.Sprintf("Available rooms:\n%s", strings.Join(rooms, "\n")))
		},
		"/help": func(c *Client, args []string) {
			help := `Available commands:
/nick <new_nickname> - Change your nickname
/msg <nickname> <message> - Send private message
/status <status_message> - Update your status
/join <room_name> - Join a chat room
/leave <room_name> - Leave a chat room
/list - List online users
/rooms - List available rooms
/help - Show this help message`
			s.sendSystemMessage(c, help)
		},
	}
}

func (s *Server) createRoom(name string) *ChatRoom {
	room := &ChatRoom{
		name:    name,
		clients: make(map[*Client]bool),
	}
	s.rooms[name] = room
	return room
}

func (s *Server) joinRoom(client *Client, roomName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	room, exists := s.rooms[roomName]
	if !exists {
		room = s.createRoom(roomName)
	}

	client.mutex.Lock()
	if client.rooms == nil {
		client.rooms = make(map[string]bool)
	}
	client.rooms[roomName] = true
	client.mutex.Unlock()

	room.clients[client] = true
	s.broadcast(Message{
		Type:      SYSTEM_MESSAGE,
		Content:   fmt.Sprintf("%s joined room: %s", client.nickname, roomName),
		Timestamp: time.Now(),
	}, nil)
}

func (s *Server) leaveRoom(client *Client, roomName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	room, exists := s.rooms[roomName]
	if !exists {
		return
	}

	delete(room.clients, client)
	client.mutex.Lock()
	delete(client.rooms, roomName)
	client.mutex.Unlock()

	s.broadcast(Message{
		Type:      SYSTEM_MESSAGE,
		Content:   fmt.Sprintf("%s left room: %s", client.nickname, roomName),
		Timestamp: time.Now(),
	}, nil)
}

func (s *Server) sendSystemMessage(client *Client, content string) {
	msg := Message{
		Type:      SYSTEM_MESSAGE,
		Content:   content,
		Timestamp: time.Now(),
	}
	s.sendMessage(client, msg)
}

func (s *Server) sendPrivateMessage(sender *Client, recipientNick, content string) {
	var recipient *Client
	for client := range s.clients {
		if client.nickname == recipientNick {
			recipient = client
			break
		}
	}

	if recipient == nil {
		s.sendSystemMessage(sender, fmt.Sprintf("User %s not found", recipientNick))
		return
	}

	msg := Message{
		Type:      PRIVATE_MESSAGE,
		Sender:    sender.nickname,
		Recipient: recipientNick,
		Content:   content,
		Timestamp: time.Now(),
	}

	s.sendMessage(recipient, msg)
	s.sendMessage(sender, msg) // Send a copy to the sender
}

func (s *Server) broadcast(msg Message, sender *Client) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Add to history
	if len(s.history) >= s.maxHistory {
		s.history = s.history[1:]
	}
	s.history = append(s.history, msg)

	for client := range s.clients {
		if client != sender {
			s.sendMessage(client, msg)
		}
	}
}

func (s *Server) sendMessage(client *Client, msg Message) {
	jsonMsg, err := json.Marshal(msg)
	if err != nil {
		return
	}
	fmt.Fprintf(client.conn, "%s\n", string(jsonMsg))
}

func handleClient(client *Client, server *Server) {
	defer func() {
		server.mutex.Lock()
		delete(server.clients, client)
		server.mutex.Unlock()
		client.conn.Close()
		server.broadcast(Message{
			Type:      SYSTEM_MESSAGE,
			Content:   fmt.Sprintf("%s has left the chat", client.nickname),
			Timestamp: time.Now(),
		}, client)
	}()

	// Send chat history
	for _, msg := range server.history {
		server.sendMessage(client, msg)
	}

	reader := bufio.NewReader(client.conn)
	for {
		message, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		message = strings.TrimSpace(message)

		if strings.HasPrefix(message, "/") {
			args := strings.Fields(message)
			if cmd, exists := server.commands[args[0]]; exists {
				cmd(client, args)
				continue
			}
		}

		server.broadcast(Message{
			Type:      PUBLIC_MESSAGE,
			Sender:    client.nickname,
			Content:   message,
			Timestamp: time.Now(),
		}, client)
	}
}

func startServer(port string) {
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
		return
	}
	defer listener.Close()

	server := NewServer()
	fmt.Printf("Server listening on port %s\n", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("Failed to accept connection: %v\n", err)
			continue
		}

		fmt.Printf("New connection from: %s\n", conn.RemoteAddr())

		client := &Client{
			conn:     conn,
			nickname: fmt.Sprintf("User-%s", conn.RemoteAddr().String()),
		}

		server.mutex.Lock()
		server.clients[client] = true
		server.mutex.Unlock()

		server.broadcast(Message{
			Type:      SYSTEM_MESSAGE,
			Content:   fmt.Sprintf("%s has joined the chat", client.nickname),
			Timestamp: time.Now(),
		}, client)
		go handleClient(client, server)
	}
}

func startClient(address string) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		fmt.Printf("Failed to connect to server: %v\n", err)
		return
	}
	defer conn.Close()

	fmt.Printf("Connected to %s\n", address)

	// Start goroutine to read server messages
	go func() {
		reader := bufio.NewReader(conn)
		for {
			message, err := reader.ReadString('\n')
			if err != nil {
				fmt.Println("Lost connection to server")
				os.Exit(1)
			}
			fmt.Print(message)
		}
	}()

	// Read user input and send to server
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fmt.Fprintf(conn, "%s\n", scanner.Text())
	}
}

func main() {
	isServer := flag.Bool("server", false, "Run as server")
	port := flag.String("port", "8080", "Port to use")
	address := flag.String("connect", "", "Address to connect to (client mode)")
	flag.Parse()

	if *isServer {
		startServer(*port)
	} else if *address != "" {
		startClient(*address)
	} else {
		fmt.Println("Please specify either -server or -connect")
		flag.PrintDefaults()
	}
}
