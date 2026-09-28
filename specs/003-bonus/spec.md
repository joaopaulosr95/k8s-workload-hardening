## Metrics endpoint + Grafana stack

## Integration tests using Kind

### NetworkIsolation

- Create 2 namespaces tenant-a and tenant-b
- Deploy two alpine containers
- Run netcat/ping against each other
- Deploy NetworkIsolation
- Check netcat/ping
- Remove NetworkIsolation
- Check if traffic is back

### Workload hardening

#### Resources

- Deploy alpine pods
  - No limits or requests
  - Only requests
  - Only limits
- Deploy LimitQuotas and check how they conflict
- Undo

#### SecurityContext

- Non-root user
- What makes a SecurityContext hardened?
- Undo
