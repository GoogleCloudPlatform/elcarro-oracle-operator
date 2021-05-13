# Appendix B: Change a Database (PDB): users/privs

El Carro provides support for declarative user/schema and roles/privilege
management through the changes in a Database manifest.

## Case 1: Add a User

In the example below we change the Database CR by adding a new user scott1 and
grant him two roles ("connect" and "resource") as well as the "unlimited
tablespace" system privilege:

```sh
cat ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: Database
metadata:
 name: pdb1
spec:
 name: pdb1
 instance: mydb
 admin_password: google
 users:
   - name: superuser
     password: superpassword
     privileges:
       - dba
   - name: scott
     password: tiger
     privileges:
       - connect
       - resource
       - unlimited tablespace
   - name: proberuser
     password: proberpassword
     privileges:
       - create session
   - name: scott1
     password: tiger
     privileges:
       - connect
       - resource
       - unlimited tablespace
```

Submit a Database CR and verify that the UsersReady condition is set to True,
which can be further confirmed by querying the data dictionary:

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml -n $NS

kubectl get databases.oracle.db.anthosapis.com -n $NS
NAME   INSTANCE   USERS                                      PHASE   DATABASEREADYSTATUS   DATABASEREADYREASON   USERREADYSTATUS   USERREADYREASON
pdb1   mydb       ["superuser","scott","proberuser","..."]   Ready   True                  CreateComplete        True              SyncComplete


SQL> alter session set container=PDB1;
Session altered.

SQL> select granted_role from dba_role_privs where grantee='SCOTT1';
GRANTED_ROLE
--------------------------------------------------------------------------------
CONNECT
RESOURCE

SQL> select privilege from dba_sys_privs where grantee='SCOTT1';
PRIVILEGE
----------------------------------------
UNLIMITED TABLESPACE
```

## Case 2: Delete a User

In the Preview release El Carro doesn't delete users (or schemas) in a database.
If a user is removed from a manifest submitted against an existing Database CR,
El Carro flags this and sets the UsersReady condition type to False.

In the example below we change the Database CR by deleting an existing user
proberuser:

```sh
cat ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: Database
metadata:
 name: pdb1
spec:
 name: pdb1
 instance: mydb
 admin_password: google
 users:
   - name: superuser
     password: superpassword
     privileges:
       - dba
   - name: scott
     password: tiger
     privileges:
       - connect
       - resource
       - unlimited tablespace
   - name: scott1
     password: tiger
     privileges:
       - connect
       - resource
       - unlimited tablespace
```

Submit a Database CR and verify that the UsersReady condition indeed gets reset
to False. You can then get more information on this by reviewing the status. You
can further query the data dictionary to confirm that indeed the user and the
privileges/roles in the database haven't changed:

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml -n $NS

kubectl get databases.oracle.db.anthosapis.com -n $NS
NAME   INSTANCE   USERS                            PHASE   DATABASEREADYSTATUS   DATABASEREADYREASON   USERREADYSTATUS   USERREADYREASON
pdb1   mydb       ["superuser","scott","scott1"]   Ready   True                  CreateComplete        False             UserOutOfSync

kubectl get databases.oracle.db.anthosapis.com pdb1 -o=jsonpath='{.status}' -n $NS
{"conditions":[{"message":"User \"PROBERUSER\" not defined in database spec, supposed to be deleted. suppressed SQL \"ALTER SESSION SET CONTAINER=PDB1; DROP USER PROBERUSER CASCADE;\". Fix by deleting the user in DB or updating DB spec to include the user","reason":"UsersOutOfSync","status":"False","type":"UsersReady"}],"status":"Ready"}


SQL> alter session set container=PDB1;

Session altered.

SQL> select privilege from dba_sys_privs where grantee='PROBERUSER';

PRIVILEGE
----------------------------------------
CREATE SESSION
```

## Case 3: Change a User by adding/removing Roles/Privileges

On top of adding and removing users, you can also add/remove privileges and
roles from/to the existing users:

```sh
cat ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml
apiVersion: oracle.db.anthosapis.com/v1alpha1
kind: Database
metadata:
 name: pdb1
spec:
 name: pdb1
 instance: mydb
 admin_password: google
 users:
   - name: superuser
     password: superpassword
     privileges:
       - unlimited tablespace
   - name: scott
     password: tiger
     privileges:
       - connect
       - resource
       - unlimited tablespace
   - name: proberuser
     password: proberpassword
     privileges:
       - connect
       - create session
   - name: scott1
     password: tiger
     privileges:
       - connect
```

Submit a Database CR and query the data dictionary to confirm that the
privileges and roles have been removed/added as requested:

```sh
kubectl apply -f ${PATH_TO_EL_CARRO_RELEASE}/samples/v1alpha1_database_pdb1.yaml -n $NS


SQL> alter session set container=PDB1;

Session altered.

SQL> select privilege from dba_sys_privs where grantee='SUPERUSER';

PRIVILEGE
----------------------------------------
UNLIMITED TABLESPACE

SQL> select granted_role from dba_role_privs where grantee='SUPERUSER';

no rows selected

SQL> select privilege from dba_sys_privs where grantee='SCOTT1';

no rows selected

SQL> select granted_role from dba_role_privs where grantee='SCOTT1';

GRANTED_ROLE
--------------------------------------------------------------------------------
CONNECT

SQL> select privilege from dba_sys_privs where grantee='PROBERUSER';

PRIVILEGE
----------------------------------------
CREATE SESSION

SQL> select granted_role from dba_role_privs where grantee='PROBERUSER';

GRANTED_ROLE
--------------------------------------------------------------------------------
CONNECT
```
