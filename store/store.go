package store

import (
	"github.com/MHS-20/poseidon/task"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/boltdb/bolt"
)


type Store interface {
 Put(key string, value interface{}) error
 Get(key string) (interface{}, error)
 List() (interface{}, error)
 Count() (int, error)
}
