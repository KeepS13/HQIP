"""Generate two exact HQIP hosts; the user origin also serves the API."""
from pathlib import Path

USER='hqip.sharevps.online'
API=USER
ADMIN='cms-hqip.sharevps.online'
CF_RANGES=('173.245.48.0/20','103.21.244.0/22','103.22.200.0/22','103.31.4.0/22','141.101.64.0/18','108.162.192.0/18','190.93.240.0/20','188.114.96.0/20','197.234.240.0/22','198.41.128.0/17','162.158.0.0/15','104.16.0.0/13','104.24.0.0/14','172.64.0.0/13','131.0.72.0/22','2400:cb00::/32','2606:4700::/32','2803:f800::/32','2405:b500::/32','2405:8100::/32','2a06:98c0::/29','2c0f:f248::/32')
BASE_CSP="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' https://"+API+"; frame-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
AUTH_CSP=BASE_CSP.replace("script-src 'self'","script-src 'self' https://challenges.cloudflare.com").replace("; frame-src 'none'"," https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com")

def http_host(host):
 return f'''server {{
 listen 80; listen [::]:80;
 server_name {host};
 access_log /var/log/nginx/hqip-control/access.log hqip_control_safe;
 error_log /dev/null crit;
 location ~ "^/\\.well-known/acme-challenge/[A-Za-z0-9_-]{{1,128}}$" {{ root /var/www/letsencrypt; try_files $uri =404; }}
 location / {{ if ($hqip_cloudflare_peer = 0) {{ return 403; }} return 301 https://{host}$request_uri; }}
}}
'''

def tls_host(host):
 return f'''server {{
 listen 443 ssl http2; listen [::]:443 ssl http2;
 server_name {host};
 ssl_certificate /etc/letsencrypt/live/{host}/fullchain.pem;
 ssl_certificate_key /etc/letsencrypt/live/{host}/privkey.pem;
 ssl_protocols TLSv1.2 TLSv1.3;
 if ($hqip_cloudflare_peer = 0) {{ return 403; }}
 access_log /var/log/nginx/hqip-control/access.log hqip_control_safe;
 error_log /dev/null crit;
 add_header Strict-Transport-Security "max-age=31536000" always;
 add_header X-Content-Type-Options nosniff always;
 add_header Referrer-Policy no-referrer always;
 add_header X-Frame-Options DENY always;
'''

def render():
 text='''# Included in nginx http; never log tokens, queries or authorization codes.
log_format hqip_control_safe '$remote_addr [$time_local] $request_method $status $body_bytes_sent $request_time';
limit_conn_zone $binary_remote_addr zone=hqip_api_connections:10m;
'''
 # realip_remote_addr retains the socket peer before CF-Connecting-IP rewriting.
 # Client supplied forwarding headers can never grant origin access.
 text+='geo $realip_remote_addr $hqip_cloudflare_peer {\n default 0;\n'
 text+=''.join(' '+cidr+' 1;\n' for cidr in CF_RANGES)+'}\n'
 text+=f'''map $request_uri $hqip_user_csp {{
 default "{BASE_CSP}";
 ~^/(login|register|authorize)(\\?|$) "{AUTH_CSP}";
}}
# Download handlers own cache policy: only verified hash URLs are immutable.
map $uri $hqip_static_cache {{ default "no-store"; ~^/assets/ "public, max-age=31536000, immutable"; ~^/downloads/ ""; }}
'''
 for host in [USER,ADMIN]:text+=http_host(host)
 for host,folder,csp in [(USER,'user-web','$hqip_user_csp'),(ADMIN,'admin-web',BASE_CSP)]:
  text+=tls_host(host)+f''' root /opt/hqip-control/current/{folder};
 index index.html;
 add_header Content-Security-Policy "{csp}" always;
 add_header Cache-Control $hqip_static_cache always;
 location = /admin {{ return 404; }}
 location ^~ /admin/ {{ return 404; }}
'''
  if host==USER:
   text+=api_proxy()
  else:
   text+=' location ~ ^/(api(/|$)|sub(/|$)|usub(/|$)|healthz$|readyz$|s(/|$)|downloads(/|$)) { return 404; }\n'
  text+=''' location ~ /\\. { return 404; }
 location ^~ /assets/ { try_files $uri =404; }
 location = /favicon.svg { try_files $uri =404; }
 location = /index.html { try_files $uri =404; }
 location / { try_files $uri /index.html; }
}
'''
 return text


def preview_routes():
 # This independently published channel survives normal control-service releases.
 headers = ''' add_header X-Content-Type-Options nosniff always;
 add_header Strict-Transport-Security "max-age=31536000" always;
 add_header Referrer-Policy no-referrer always;
 add_header X-Frame-Options DENY always;
'''
 text=' # BEGIN HQIP Flutter preview\n'
 for api, filename in [('/api/v3/client/releases','releases.json'),('/api/v1/user/installers','installers.json')]:
  text+=f''' location = {api} {{
  if ($arg_client = flutter) {{ rewrite ^ /__hqip_flutter_preview/{filename} last; }}
  proxy_pass http://127.0.0.1:18091;
 }}
 location = /__hqip_flutter_preview/{filename} {{
  internal;
  if ($arg_channel != preview) {{ return 404; }}
  limit_except GET HEAD {{ deny all; }}
  alias /opt/hqip-flutter-preview/current/{filename};
  default_type application/json;
  add_header Cache-Control "no-store" always;
{headers} }}
'''
 text+=''' location ~ "^/downloads/flutter/preview/([a-f0-9]{64})/(HQIP-Preview-(Windows-x64(-Setup\\.exe|\\.zip)|Android-(arm64|x64)\\.apk))$" {
  limit_except GET HEAD { deny all; }
  alias /opt/hqip-flutter-preview/objects/$1/$2;
  disable_symlinks on;
  default_type application/octet-stream;
  etag off;
  add_header ETag '"$1"';
  add_header Content-Disposition "attachment";
  add_header Cache-Control "public, max-age=31536000, immutable";
'''+headers+''' }
 location ~ ^/downloads/flutter/preview/ { return 404; }
 location /__hqip_flutter_preview/ { return 404; }
 # END HQIP Flutter preview
'''
 return text


def api_proxy():
 text=''.join(' set_real_ip_from '+cidr+';\n' for cidr in CF_RANGES)
 text+=''' real_ip_header CF-Connecting-IP;
 real_ip_recursive off;
 limit_conn hqip_api_connections 200;
 limit_conn_status 429;
 client_header_timeout 15s;
 client_body_timeout 15s;
 send_timeout 30s;
 reset_timedout_connection on;
 client_max_body_size 10m;
 proxy_http_version 1.1;
 proxy_set_header Host $host;
 proxy_set_header X-Real-IP $remote_addr;
 proxy_set_header X-Forwarded-For $remote_addr;
 proxy_set_header X-Forwarded-Proto https;
 proxy_set_header Connection "";
 proxy_buffering off; proxy_cache off;
 proxy_read_timeout 3600s;
 location = /api/v1/payments/fm/notify { client_max_body_size 16k; proxy_pass http://127.0.0.1:18091; }
 location ~ ^/(api/v1/(client|agents|user)(/|$)|api/v[23]/client(/|$)|sub/|usub/|healthz$|readyz$|downloads/) { proxy_pass http://127.0.0.1:18091; }
 location /api/ { proxy_pass http://127.0.0.1:18090; }
 location = /api { return 404; }
 location = /sub { return 404; }
 location = /usub { return 404; }
 location = /downloads { return 404; }
 location = /s { return 404; }
 location /s/ { return 404; }
'''
 return preview_routes()+text

if __name__=='__main__':
 (Path(__file__).resolve().parents[1]/'deploy/nginx-hqip.conf').write_text(render(),encoding='utf-8')
