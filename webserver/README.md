### A simple webserver based on go with templating feature 

The templating feature based on 'text/template'. 
Additionally all environment variable are available too as well as the current date as ```.Date``` (see example below).

The template can either be passed as a flag: ```-template='{"date": "{{.Date}}"}"'```

or as an environment variable: ```TEMPLATE='{"date": "{{.Date}}"}"' ./simple-webserver```

