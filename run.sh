POSEIDON_WORKER_HOST=localhost \
POSEIDON_WORKER_PORT=5555 \
POSEIDON_MANAGER_HOST=localhost \
POSEIDON_MANAGER_PORT=5556 \
go run main.go

# curl -v --request POST \
# --header 'Content-Type: application/json' \
# --data @task.json \
# localhost:7778/tasks

# curl -v --request DELETE \
#  'localhost:7778/tasks/21b23589-5d2d-4731-b5c9-a97e9832d021'
