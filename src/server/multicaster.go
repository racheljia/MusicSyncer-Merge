package main 

import (
	"fmt"
	"net"
	"time"
	"log"
	"net/rpc"
	"sync"
)

/*
 * Message passing structure
 */

type ElectionMsg struct{
	// Vote, Already vote or candidate, announce
	Type string
	NewMaster string
}

type ListContent struct {
	ListName string
	Type string
	Pos string
	File string
}  

type Message struct {
	Dest string
	Src string
	Type string
	RemMemName string 	// remMemName can be either "data" or remove member name depends on Type
	ListInfo ListContent
	ElectionInfo ElectionMsg
}

type RPCRecver struct {
	rcvMedia *Mulitcaster
	ackChans map[string]chan string
}

type Mulitcaster struct {
	members map[string]string
	myInfo Server
	myId string
	msgChans map[string]chan ListContent 	// key: MusicList name, value: new message
	ackChans map[string]chan string 	// Key: MusicList name, value: Ack messages
	elecChan chan ElectionMsg
	sender RPCRecver
	/* election */
	voted bool		// Set true if already vote for a candidate
	numVotes int 	// Number of vote recieved
	masterChan chan string 	// New master channel
	electionLock *sync.Mutex
}

// All should return 'success' if communicate successfully
func (this *RPCRecver) Communicate (msg Message, reply *string) error{
	*reply = ""
	switch msg.Type {
	case "remMem":
		// Only master can remove member
		fmt.Println("Remove member: ", msg.RemMemName)
		this.rcvMedia.RemoveMemberLocal(msg.RemMemName)
		*reply = "success"
	case "election":
		this.rcvMedia.elecChan <- msg.ElectionInfo
		*reply = "success"
	case "listMsg":
		*reply = "false"
		// Slaves recieves messages, send an ack back to the master
		msg := Message{msg.Src, this.rcvMedia.myInfo.ip+":"+this.rcvMedia.myInfo.comm_port, "ackRcv", "", msg.ListInfo, msg.ElectionInfo}
		go this.rcvMedia.SendMsg(msg)
		// wait for master to send a commit log back, if timeout discard the msg

		// New list 
		if msg.ListInfo.Type == "create" {
			this.rcvMedia.msgChans[msg.ListInfo.ListName] = make(chan ListContent)
			this.rcvMedia.ackChans[msg.ListInfo.ListName] = make (chan string)
			this.ackChans[msg.ListInfo.ListName] = make (chan string)
		}
		select {
			case <-this.ackChans[msg.ListInfo.ListName]:
				*reply = "success"
				// rcv commit message from master, delivers
				this.rcvMedia.msgChans[msg.ListInfo.ListName] <- msg.ListInfo
				fmt.Println("Deliver msg success")
			case <- time.After(time.Second * 1):
				if msg.ListInfo.Type == "create" {
					delete(this.rcvMedia.msgChans, msg.ListInfo.ListName)
					delete(this.rcvMedia.ackChans, msg.ListInfo.ListName)
					delete(this.ackChans, msg.ListInfo.ListName)
				}
				fmt.Println("time out")
		}
	// TODO: when a client upload file to the server, shard and replica and sending to corresponding servers
	case "uploadFile":
		*reply = "success"
	case "ackRcv":
		// Master will rcv this type of msg if slave recv a file and send back an ack
		this.rcvMedia.ackChans[msg.ListInfo.ListName] <- "ack"
	case "commit":
		this.ackChans[msg.ListInfo.ListName] <- "ack"
	default:
		fmt.Println("Message type not correct: ", msg.Type)
		*reply = ""
	}
	return nil
}


/*
 * Below are how we handle the rpc for multicasting
 */

func (this *Mulitcaster) Initiallized(server Server, members []Server){
	this.members = make(map[string]string)
	for s := range members {
		// TODO: add server name as key and server ip + comm_port as server
		this.members[members[s].name] = members[s].combineAddr("comm")
	}
	this.myInfo = server
	this.sender = RPCRecver{this, make(map[string]chan string)}

	this.voted = false
	this.numVotes = 0
	this.masterChan = make(chan string)

	this.ackChans = make(map[string]chan string)
	this.msgChans = make(map[string]chan ListContent)
	this.elecChan = make(chan ElectionMsg, 128)
	this.electionLock = new(sync.Mutex)
	go this.lisenter(server)
}

/*
 * Start the listener which runs the multicaster 
 */
func (this *Mulitcaster) lisenter(server Server){
	rpc.Register(&(this.sender))
	l, e := net.Listen("tcp", ":"+server.comm_port)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	for{
		c, e := l.Accept()
		if e != nil {
			log.Fatal("client connect error: ", e)
		}
		go rpc.ServeConn(c)
	}
}

/* Multicast a update List message to inform update to all the members,
	Only master can use this function */
func (this *Mulitcaster) UpdateList(content ListContent) bool {
	msg := Message{"", this.myInfo.ip+":"+this.myInfo.comm_port, "listMsg", "", content, ElectionMsg{}}

	// New list, Create new channel to rcv messages
	if msg.ListInfo.Type == "create" {
		this.msgChans[content.ListName] = make(chan ListContent)
		this.ackChans[content.ListName] = make (chan string)
		this.sender.ackChans[content.ListName] = make (chan string)
	}

	for key := range this.members {
		if key == this.myInfo.name {
			continue
		}
		msg.Dest = this.members[key]
		go this.SendMsg(msg)
	}

	// Request message from 
	numVote := int(len(this.members)/2)
	numRcv := 0
	for i:=0; i<numVote-1; i++ {
		select {
			case <- this.ackChans[content.ListName]:
				numRcv += 1
				fmt.Println("rcv Ack ", numRcv)
			case <- time.After(time.Millisecond * 600):
				fmt.Println("Not enough vote :(")
				if content.Type == "create" {
					delete(this.msgChans, msg.ListInfo.ListName)
					delete(this.ackChans, msg.ListInfo.ListName)
					delete(this.sender.ackChans, msg.ListInfo.ListName)
				}
				return false
		}
	}
	// Rcv a majority of votes, multicast commit message to those slaves
	msg.Type = "commit"
	for key := range this.members {
		if key == this.myInfo.name {
			continue
		}
		msg.Dest = this.members[key]
		go this.SendMsg(msg)
	}
	// Delivers to itself
	this.msgChans[content.ListName] <- msg.ListInfo
	return true
}

func (this *Mulitcaster) GetElecChan() chan ElectionMsg {
	return this.elecChan
}

func (this *Mulitcaster) GetMsgChans(server string) chan ListContent {
	return this.msgChans[server]
} 

func (this *Mulitcaster) SendMsg(msg Message) {
	// fmt.Println()
	c, err := rpc.Dial("tcp", msg.Dest)
	if err != nil {
		return
	}
	defer c.Close()
	var result string
	c.Call("PasserRPC.Communicate", msg, &result)
}

func (this *Mulitcaster) RemoveMemberLocal(memberName string){
	delete(this.members, memberName)
}

// Return true if success, else return false. Only for master
func (this *Mulitcaster) RemoveMemberGlobal(memberName string) bool{
	this.RemoveMemberLocal(memberName)
	msg := Message{"", this.myInfo.combineAddr("comm"), "remMem", memberName, ListContent{}, ElectionMsg{}}
	for key := range this.members{
		if key == this.myInfo.name {
			continue
		}
		msg.Dest = this.members[key]
		go this.SendMsg(msg)
	}
	return true
}

/* send election message, ask group memeber to vote for me */
func (this *Mulitcaster) SendElectionMsg(oldMaster string) bool{
	this.electionLock.Lock()
	if this.voted == true {
		fmt.Println("I've already voted, cannot be a candidate :(")
		this.electionLock.Unlock()
		return false 
	}
	this.voted = true
	for key := range this.members{
		if key == this.myInfo.name || key == oldMaster{
			continue
		}
		msg := Message{this.members[key], this.myInfo.ip+":"+this.myInfo.comm_port, "election", "", ListContent{}, ElectionMsg{"candidate", this.myInfo.name}}
		go this.SendMsg(msg)
	}
	this.electionLock.Unlock()
	return true
}

/* send announce message to told everyone I'm the new master */
func (this *Mulitcaster) SendNewMasterMsg() {
	this.electionLock.Lock()
	for key := range this.members {
		if key == this.myInfo.name {
			continue
		}
		msg := Message{this.members[key], this.myInfo.ip+":"+this.myInfo.comm_port, "election", "", ListContent{}, ElectionMsg{"announce", this.myInfo.name}}
		go this.SendMsg(msg)
	}
	this.electionLock.Unlock()
}

/* send election message, I will vote or not vote for you */
func (this *Mulitcaster) SendVoteMessage(msg ElectionMsg) {
	this.electionLock.Lock()
	tmsg := Message{}
	if this.voted == true {
		tmsg = Message{this.members[msg.NewMaster], this.myInfo.ip+":"+this.myInfo.comm_port, "election", "", ListContent{}, ElectionMsg{"vote", msg.NewMaster}}
	} else {
		tmsg = Message{this.members[msg.NewMaster], this.myInfo.ip+":"+this.myInfo.comm_port, "election", "", ListContent{}, ElectionMsg{"novote", msg.NewMaster}}
		this.voted = true
	}
	go this.SendMsg(tmsg)
	this.electionLock.Unlock()
}
