# El Carro data migration guide

This guide helps you migrate an Oracle (12.2 or 19.3) CDB database into an El Carro
instance. We provided two categories of data migration solutions.

## Automated El Carro Data Migration

El Carro helps you perform and monitor data migration, they are convenient but
less flexible.

[Automated Data Migration using Data Guard](automated-dataguard.md)

[Automated Data Migration using Data Pump](automated-datapump.md)

## Manual data migration with playbooks

This category gives max flexibility, you can pick existing data migration
playbooks to integrate with El Carro.

The playbooks below were prepared as examples.

[Manual Data Migration through RMAN Backup](manual-rman.md)

[Manual Data Migration through Data Pump](manual-datapump.md)

To integrate with other playbooks, see above examples and tips in
[El Carro database environment](../database-env.md).

## Migration Approaches Comparison Chart

Depending upon the scope and environments, actual results may vary. A proof of
concept is recommended to finalize the data migration approach that will be used
to migrate to El Carro.

| Category  | Options                                                                       | migration downtime | Direct connection to source database | Scalability | User complexity |
|-----------|-------------------------------------------------------------------------------|--------------------|--------------------------------------|-------------|-----------------|
| Automated | Operator automated Data Guard physical standby                                | low                | yes                                  | high        | low             |
| Automated | Operator automated data pump                                                  | high               | no                                   | low         | low             |
| Manual    | Data pump migration playbook                                                  | high               | no                                   | low         | low             |
| Manual    | RMAN backup migration playbook                                                | high               | no                                   | medium      | medium          |
| Manual    | Other playbooks (For example Golden Gate, Transportable Tablespace playbooks) | -                  | -                                    | -           | -               |

Migration downtime: required downtime to migrate source database into El Carro
without data loss. Low means less downtime, high means more downtime.

Direct connection to source DB: whether the solution requires direct network
connection to source DB.

Scalability : how scalable the option is, when data volume is increased in terms
of number of records as well as number of entities/tables to be migrated.

User complexity: how many and how complex the manual steps are required to
perform the migration.
