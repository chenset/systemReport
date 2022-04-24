FROM alpine:3.15

ADD ./systemReport /root/systemReport

# usage
# bash ./build.sh
# docker build . -t ccr.ccs.tencentyun.com/oooo/system-report:latest
# docker push ccr.ccs.tencentyun.com/oooo/system-report:latest
