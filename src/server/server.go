package main

import (
	"net/http"
    "fmt"
    "strings"
    "html/template"
    "io"
    "time"
    "crypto/md5"
    "strconv"
    "os"
)

var (
    dir string
    servers []Server //all servers and port
    masterServer map[string]Server
    
    myServer Server //this server info
    heartBeatTracker = new(HeartBeat) //heart beat related
    
    musicList []MusicList //local groups music list
    hasGroups map[string]bool //local groups map
    clusterMap map[string][]Server //key:cluster name, value:cluster's server list
    //clusterMap map[string][]string
    groupMap map[string]string //key: groupName, value: cluster name

    /* New */
    master Server
    multicaster Mulitcaster
)

type Content struct{
	Test string
}

type Server struct {
    ip string
	name string
    comm_port string
    http_port string
    heartbeat_port string
    cluster string
    heartbeatFreq int
}

func (s Server) combineAddr(port string) string{
	switch port {
		case "comm": return s.ip + ":" + s.comm_port
		case "http": return s.ip + ":" + s.http_port
		case "heartbeat": return s.ip + ":" + s.heartbeat_port
	}
	return ""
}


func createHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method == "GET" {
    	t, _ := template.ParseFiles("UI/create.html")
    	t.Execute(w,nil)
    } else if r.Method == "POST" {
    	r.ParseForm()
    	groupName := strings.TrimSpace(r.PostFormValue("groupname"))
    	if !isGroupNameExist(groupName) {
    		createNewGroupLocal(groupName, myServer.cluster) //local
    		multicastServers(groupName, "create_group") //check group type			
			http.Redirect(w, r, "/upload.html", http.StatusFound)
    	} else {
    		w.Write([]byte("group name exist, please try another"))
    	}
    } else {
    	fmt.Fprintf(w, "Error Method")
    }    
}

func joinHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method == "GET" {
    	//TODO: get music list file and render to join.html
    	t, _ := template.ParseFiles("UI/join.html")
    	t.Execute(w,nil)
    } else if r.Method == "POST"{
    	r.ParseForm()
    	groupName := strings.TrimSpace(r.PostFormValue("groupname"))
    	if isGroupHere(groupName) {
    		w.Write([]byte("you are in the group: " + groupName))
    		
    		//TODO: go to listen music page with group name
    		t, _ := template.ParseFiles("UI/upload.html")
    		t.Execute(w,nil)    		
    	} else {  		
    		redirectToCorrectServer(groupName,w,r) //didn't check
    		w.Write([]byte("join successful"))    		
    	}    	
    } else {
    	fmt.Fprintf(w, "Error Method")
    }  
}

func redirectToCorrectServer(groupName string, w http.ResponseWriter, r *http.Request) {
	serverList := clusterMap[groupMap[groupName]]
	http.Redirect(w,r, serverList[0].combineAddr("http")+"/upload.html", http.StatusFound)
}

func addfileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
        hasher := md5.New()
        io.WriteString(hasher, strconv.FormatInt(time.Now().Unix(), 10))
        token := fmt.Sprintf("%x", hasher.Sum(nil))
        t, _ := template.ParseFiles("UI/upload.html")
        t.Execute(w, token)
    } else if r.Method == "POST" {
    	fmt.Println("Upload Post")
        r.ParseMultipartForm(32 << 20)
        file, handler, err := r.FormFile("uploadfile")
        groupName := strings.TrimSpace(r.PostFormValue("groupname"))
        if err != nil {
        	http.Redirect(w, r, "/upload.html", http.StatusFound)
            fmt.Println(err)
            return
        }
        // defer file.Close()
        fmt.Println("[upload] file name: ",handler.Filename)
        f, err := os.OpenFile("./test/"+handler.Filename, os.O_WRONLY|os.O_CREATE, 0666)
        
        //TODO: check
        mList := getMusicList(groupName)
        fmt.Println("MList: ", mList)
        mList.Add(handler.Filename, getServerListByClusterName(myServer.cluster))
        
        if err != nil {
            fmt.Println(err)
            return
        }
        // defer f.Close()
        
        io.Copy(f, file)
        fmt.Println("Upload success")
        // http.Redirect(w, r, "/upload.html", 301)
    }
}


func homeHandler(w http.ResponseWriter, r *http.Request) { //clear
	if r.Method == "GET" {
		//c := Content{Test: "test!!!!!"}
    	t, _ := template.ParseFiles("UI/index.html")
    	t.Execute(w, nil)
    } else {
    	fmt.Fprintf(w, "Error Method")
    }
}

func startHTTP() {
	fmt.Println("[init] HTTP at port", myServer.http_port)
  	
  	http.Handle("/css/", http.FileServer(http.Dir("UI")))
    http.Handle("/js/", http.FileServer(http.Dir("UI")))
    http.Handle("/images/", http.FileServer(http.Dir("UI")))
    http.Handle("/fonts/", http.FileServer(http.Dir("UI")))
    
	http.HandleFunc("/index.html", homeHandler)
	http.HandleFunc("/create.html", createHandler)
    http.HandleFunc("/join.html", joinHandler)
    http.HandleFunc("/upload.html", addfileHandler)
    //http.HandleFunc("/leave", leaveHandler)
    
    http.ListenAndServe(":" + myServer.http_port, nil)
   
}

func getDeadServer(){ 
    //fmt.Println("Get dead servers from heartbeat manager's deadchannel")
    deadServerChannel := heartBeatTracker.GetDeadChannel()
    
    for {
		dead := <-deadServerChannel
		fmt.Println("[Heart Beat] dead: ", dead)
        // If I'm the master, then I must detect some slave died
        // Told every slaves to update their server list
        if master == myServer {
            memToRemove := myServer
            for i:= range servers {
                if clusterMap[myServer.cluster][i].combineAddr("heartbeat") == dead {
                    memToRemove = servers[i]
                    break
                }
            }
            if master == memToRemove {
                fmt.Println("Can not found dead server within the list", dead)
                return
            }
            // Tell other slaves to remove this slave from their list
            multicaster.RemoveMemberGlobal(memToRemove.combineAddr("comm"))
            //remove dead server from map
            rmDeadServer(memToRemove)
            
        } else {
        // If I'm the client which detects the master is dead
        // Become a candidate and raise election
       
            // raise an election
            multicaster.SendElectionMsg(master.combineAddr("comm"))
            // Wait for others to vote for you
            select {
                case newMaster := <-multicaster.masterChan:
                	rmDeadServer(master)
                    UpdateMaster(newMaster)
                case <- time.After(time.Millisecond * 500):
                    fmt.Println("time out in getting a new master")
            }
        }   

    }
}

func rmDeadServer(memToRemove Server) {
	list := clusterMap[memToRemove.cluster]
	for i:= range list {
		if list[i] == memToRemove{
			list = append(list[:i],list[i+1:]...)
			break
		}
	}
}


// TODO: update the list in heartbeat and server.go
func UpdateMaster(new_master string) {
    multicaster.numVotes = 0
    multicaster.voted = false
    if myServer.name == new_master {
        master = myServer
        // TODO
        // heartbeat.updateAliveList()
    } else {
        for i := range servers {
            if servers[i].name == new_master {
                master = servers[i]
                break
            }
        }
        // TODO
        // heartbeat.updateAliveList()
    }
}

func GetElecMsg() {
    for {
    	eMsg := <-multicaster.elecChan        
    	switch eMsg.Type {
            case "candidate":
                go multicaster.SendVoteMessage(eMsg)
            case "announce":
            	rmDeadServer(master)
                UpdateMaster(eMsg.NewMaster)
                fmt.Println("Somebody else is the new master!")
            case "vote":
                multicaster.numVotes += 1
                if multicaster.numVotes == int((len(servers)-1)/2) {
                    // Delivers message to itself
                    multicaster.masterChan <- eMsg.NewMaster
                    multicaster.RemoveMemberGlobal(master.name)
                    multicaster.SendNewMasterMsg()
                    UpdateMaster(eMsg.NewMaster)
                }
            case "novote":
        }
    }
}

func main() {
	readServerConfig() 
	
	//select server's configuration
    fmt.Print("[init] Enter a number(0-3) set up this server: ")
    var i int
    fmt.Scan(&i)
    myServer = servers[i]
    master = masterServer[myServer.cluster]

	readGroupConfig()
	readMusicConfig()
	
	//InitialHeartBeat(master)
	//go getDeadServer()
    //go GetElecMsg()

	go listeningMsg()
    startHTTP()
}

/*func leaveHandler(w http.ResponseWriter, r *http.Request) {
    //fmt.Fprintln(w, "<h1>%s!</h1>", r.URL.Path[1:])
    r.ParseForm()
    if r.Method == "GET" {
    	fmt.Fprintf(w, "Error Method")
    } else {
    	ip := strings.TrimSpace(r.PostFormValue("clientip"))
    	groupid := strings.TrimSpace(r.PostFormValue("groupid"))
    	comMainGroup(groupid, "remove_server")
    	//leaveGroup(ip, groupid)
    } 
}*/
