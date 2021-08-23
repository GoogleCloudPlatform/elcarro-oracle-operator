# El Carro Operator installation guide

El Carro is a new tool that allows users to keep full control of their database
environment (root on a machine, sysdba in Oracle), while helping users automate
several aspects of managing their database services.

El Carro helps you with the deployment and management of a database software
(like Oracle database) on Kubernetes. You must have appropriate licensing rights
to that database software to allow you to use it with El Carro (BYOL).

El Carro supports three major Oracle database versions: **12c EE**, **19c EE**
and **18c XE**. Oracle 18c XE is free to use. El Carro provides support for
database images built using El Carro image build scripts and
[Oracle scripts](https://github.com/oracle/docker-images/tree/main/OracleDatabase/SingleInstance).
El Carro also supports database images found on
[Oracle Container Registry](https://container-registry.oracle.com).

-   For Oracle 12c EE, check out the
    [quickstart guide for Oracle 12c EE](quickstart-12c-ee.md)
-   For Oracle 19c EE, check out the
    [quickstart guide for Oracle 19c EE](quickstart-19c-ee.md)
-   For Oracle 18c XE (Express Edition), check out the
    [quickstart guide for Oracle 18c XE](quickstart-18c-xe.md)
