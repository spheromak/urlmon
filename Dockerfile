FROM busybox
MAINTAINER Jesse Nelson <spheromak@gmail.com>

ADD urlmon /bin/urlmon

EXPOSE 9731

ENTRYPOINT /bin/urlmon
