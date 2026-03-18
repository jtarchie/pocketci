# Drivers API

List and query available orchestration drivers.

## List Drivers

`GET /api/drivers`

Returns available drivers configured on the server.

```bash
curl http://localhost:8080/api/drivers
```

Response:

```json
{
  "drivers": [
    {
      "name": "docker",
      "status": "available"
    },
    {
      "name": "native",
      "status": "available"
    }
  ]
}
```

The response reflects the `--allowed-drivers` configured on the server.
