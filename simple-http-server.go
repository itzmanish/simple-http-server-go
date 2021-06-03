package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type key int

type messageType struct {
	Id        string `json:"id"`
	Message   string `json:"message"`
	Timestamp string `json:"created_at"`
}

const (
	requestIDKey key = 0
)

var (
	port       string
	access_key string
	mysql_dsn  string
	healthy    int32
)

var once sync.Once

func main() {
	flag.StringVar(&port, "port", "8081", "server listen address")
	flag.StringVar(&access_key, "access_key", "c29NZVN1cGVSYW5kb21BbmRTM2NSM3RLM3k=", "Access key for allowing user to post message")
	flag.StringVar(&mysql_dsn, "mysql_dsn", "", "DSN of mysql db to connect to.")

	flag.Parse()

	logger := log.New(os.Stdout, "Simple server: ", log.LstdFlags)
	logger.Println("Server is starting...")

	router := http.NewServeMux()
	router.Handle("/", index())
	router.Handle("/add", addMessage())
	router.Handle("/messages", listMessages())
	router.Handle("/health", healthz())

	nextRequestID := func() string {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}

	server := &http.Server{
		Addr:        ":" + port,
		Handler:     http.TimeoutHandler(tracing(nextRequestID)(logging(logger)(router)), 5*time.Second, "Timeout! Server is taking unexpected amount of time to respond."),
		ErrorLog:    logger,
		ReadTimeout: 5 * time.Second,
		IdleTimeout: 15 * time.Second,
	}

	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)

	go func() {
		<-quit
		logger.Println("Server is shutting down...")
		atomic.StoreInt32(&healthy, 0)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		server.SetKeepAlivesEnabled(false)
		if err := server.Shutdown(ctx); err != nil {
			logger.Fatalf("Could not gracefully shutdown the server: %v\n", err)
		}
		close(done)
	}()

	logger.Println("Server is ready to handle requests at", port)
	atomic.StoreInt32(&healthy, 1)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("Could not listen on %s: %v\n", port, err)
	}

	<-done
	logger.Println("Server stopped")
}

func index() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{version: 'v1.0.0', message: 'Hey, I am up and alive!'}")
	})
}

func healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&healthy) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
}

func addMessage() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			access := r.URL.Query().Get("access_key")
			if len(access) == 0 {
				http.Error(rw, "Access key is required to send a message", http.StatusUnauthorized)
				return
			}
			if access != access_key {
				http.Error(rw, "Access key is not valid", http.StatusUnauthorized)
				return
			}
			var msg messageType
			err := json.NewDecoder(r.Body).Decode(&msg)
			if err != nil {
				http.Error(rw, "Unable to read body!", http.StatusBadRequest)
				return
			}
			if len(msg.Message) == 0 {
				http.Error(rw, "Message is required!", http.StatusBadRequest)
				return
			}
			db, err := initDB()
			if err != nil {
				http.Error(rw, "Unable to connect to db", http.StatusInternalServerError)
				return
			}
			defer db.Close()
			// Prepare statement for inserting data
			stmtIns, err := db.Prepare("INSERT INTO messages(message) VALUES(?)") // ? = placeholder
			if err != nil {
				http.Error(rw, "Unable to prepare statement", http.StatusInternalServerError)
				return
			}
			defer stmtIns.Close() // Close the statement when we leave main() / the program terminates
			_, err = stmtIns.Exec(msg.Message)
			if err != nil {
				http.Error(rw, "Unable to insert message", http.StatusInternalServerError)
				return
			}
			fmt.Fprintln(rw, msg.Message, "is inserted.")
			return
		}
		fmt.Fprintln(rw, "Only POST method is allowed!")
	})
}

func listMessages() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		db, err := initDB()
		if err != nil {
			http.Error(rw, "Unable to connect to db", http.StatusInternalServerError)
			return
		}
		defer db.Close()
		stmt, err := db.Prepare("SELECT * from messages")
		if err != nil {
			http.Error(rw, "Unable to prepare statement", http.StatusInternalServerError)
			return
		}
		var out []messageType
		rows, err := stmt.Query()
		if err != nil {
			http.Error(rw, "Unable to get messages from db", http.StatusInternalServerError)
			return
		}
		for rows.Next() {
			var temp messageType
			err = rows.Scan(&temp.Id, &temp.Message, &temp.Timestamp)
			if err != nil {
				log.Println(err)
				http.Error(rw, "Unable to get messages from db", http.StatusInternalServerError)
				return
			}
			out = append(out, temp)
		}

		json.NewEncoder(rw).Encode(out)
	})
}

func logging(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				requestID, ok := r.Context().Value(requestIDKey).(string)
				if !ok {
					requestID = "unknown"
				}
				logger.Println(requestID, r.Method, r.URL.Path, r.RemoteAddr, r.UserAgent())
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func tracing(nextRequestID func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := r.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = nextRequestID()
			}
			ctx := context.WithValue(r.Context(), requestIDKey, requestID)
			w.Header().Set("X-Request-Id", requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func initDB() (*sql.DB, error) {
	db, err := sql.Open("mysql", mysql_dsn)
	if err != nil {
		return nil, err
	}
	err = db.Ping()
	if err != nil {
		return nil, err
	}
	// See "Important settings" section.
	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	once.Do(func() {
		stmt, err := db.Prepare("CREATE table messages(id int NOT NULL AUTO_INCREMENT, message varchar(500) NOT NULL, timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (id));")
		if err != nil {
			log.Println(err)
		}
		_, err = stmt.Exec()
		if err != nil {
			log.Println(err)
		}
	})
	return db, nil
}
