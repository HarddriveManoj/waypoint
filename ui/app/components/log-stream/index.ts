import Component from '@glimmer/component';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';
import { later } from '@ember/runloop';

import ApiService from 'waypoint/services/api';
import { inject as service } from '@ember/service';
import { GetLogStreamRequest, LogBatch } from 'waypoint-pb';
import { formatRFC3339 } from 'date-fns';

interface LogStreamArgs {
  req: GetLogStreamRequest;
}

export default class LogStream extends Component<LogStreamArgs> {
  @service api!: ApiService;

  @tracked lines: string[];
  @tracked isFollowingLogs = true;
  @tracked badgeCount = 0;

  constructor(owner: any, args: any) {
    super(owner, args);
    this.lines = [];
    this.start();
  }

  addLine(line: string) {
    this.lines = [...this.lines, line];
    if (this.isFollowingLogs === false) {
      this.badgeCount = this.badgeCount + 1;
    }
  }

  @action
  initialScroll(element: any) {
    later(() => {
      element.scrollIntoView();
    }, 100);
  }

  @action
  followLogs(element: any) {
    element.target.parentElement.scroll(0, element.target.parentElement.scrollHeight);
  }

  @action
  newLineAdded(element: any) {
    if (this.isFollowingLogs === true) {
      element.scrollIntoView();
      this.badgeCount = 0;
    }
  }

  @action
  onScroll(element: any) {
    if (element.target.scrollHeight - element.target.offsetHeight === element.target.scrollTop) {
      this.isFollowingLogs = true;
      this.badgeCount = 0;
    } else {
      this.isFollowingLogs = false;
    }
  }

  async start() {
    const onData = (response: LogBatch) => {
      response.getLinesList().forEach((entry) => {
        const prefix = formatRFC3339(entry.getTimestamp()!.toDate());
        this.addLine(`${prefix}: ${entry.getLine()}`);
      });
    };

    const onStatus = (status: any) => {
      this.addLine(status.details);
    };

    var stream = this.api.client.getLogStream(this.args.req, this.api.WithMeta());

    stream.on('data', onData);
    stream.on('status', onStatus);
  }
}
