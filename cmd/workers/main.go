package main

import "github.com/Gregory-Pattrick/go-wagering-service/internal/bootstrap"

func main() {
	bootstrap.NewWorkers(bootstrap.ObserveWorkers()).Run()
}
